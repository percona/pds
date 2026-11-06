package kube

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

const (
	// SupportNamespace is where the join script put the access workload's roles,
	// and so where the workload runs.
	SupportNamespace = "pds-support"

	// supportProxyImage is the proxy a window runs. It is the vendor's proxy
	// that serves the Kubernetes API on its own, so no operator and no
	// key-minting credential has to be in the environment.
	supportProxyImage = "tailscale/k8s-proxy:v1.104.2"

	// SupportWindowLabel marks every object a window created, so removing a
	// window needs nothing but its id.
	SupportWindowLabel       = "pds.percona.com/support-window"
	supportExpiresAnnotation = "pds.percona.com/support-expires-at"
	// supportHostAnnotation names the Secret a window's proxy keeps its
	// certificate in, so the agent can tell whether it has one.
	supportHostAnnotation = "pds.percona.com/support-host"

	supportProxyName   = "pds-support-proxy"
	supportConfigName  = "pds-support-config"
	supportStateName   = "pds-support-state"
	supportConfigKey   = "config.json"
	supportConfigMount = "/etc/pds-support"

	// supportReadRole and supportAdminRole are the only roles a window may be
	// bound to. The join script gives the agent bind on exactly these two.
	supportReadRole  = "pds-support-read"
	supportAdminRole = "pds-support-admin"

	// supportGroup is the group the private network's policy maps engineers to.
	// The proxy impersonates the engineer into it, and Kubernetes authorizes that
	// identity rather than the proxy's account, so a window's binding names it.
	supportGroup = "pds.percona.com:support"
)

var errNotWindowsWorkload = errors.New("the workload belongs to another window")

// SupportWorkload is one window's access workload. Key is the single-use key
// the proxy joins the private network with: it goes into the config Secret and
// nowhere else. Host is the address's full DNS name, which names the Secret the
// proxy keeps its certificate in.
type SupportWorkload struct {
	Window    string
	Level     string
	Address   string
	Host      string
	Key       string
	ExpiresAt time.Time
}

// SupportWindow is a window whose workload is on the environment. Host is the
// address's DNS name, empty for a window with no address.
type SupportWindow struct {
	ID        string
	ExpiresAt time.Time
	Host      string
}

// InstallSupportWorkload creates what a window needs, or brings what is there
// up to date: running it twice for one window leaves one workload, restarted
// when the key changed. The state and certificate Secrets are made empty once
// and then left to the proxy, which may update the certificate Secret but not
// create it.
func (c *Client) InstallSupportWorkload(ctx context.Context, w SupportWorkload) error {
	if err := c.available(); err != nil {
		return err
	}
	role, err := supportRole(w.Level)
	if err != nil {
		return err
	}
	config, err := supportConfig(w)
	if err != nil {
		return err
	}
	labels := map[string]string{
		"app.kubernetes.io/name":    supportProxyName,
		"app.kubernetes.io/part-of": "pds",
		SupportWindowLabel:          w.Window,
	}

	core := c.clientset.CoreV1()
	account := &corev1.ServiceAccount{ObjectMeta: supportMeta(supportProxyName, labels)}
	if _, err := core.ServiceAccounts(SupportNamespace).Create(ctx, account, metav1.CreateOptions{}); err != nil &&
		!apierrors.IsAlreadyExists(err) {
		return supportError("create", "service account", supportProxyName, err)
	}

	empty := []*corev1.Secret{{ObjectMeta: supportMeta(supportStateName, labels), Type: corev1.SecretTypeOpaque}}
	// A hub with no private network has no address, so no host and no
	// certificate to keep. The certificate belongs to the address, so it
	// carries no window label: removing a window leaves it for the next one,
	// which then asks the issuer for nothing. The issuer limits how often one
	// name is issued a certificate, and each request can fail on its own.
	if w.Host != "" {
		certLabels := maps.Clone(labels)
		delete(certLabels, SupportWindowLabel)
		empty = append(empty, &corev1.Secret{ObjectMeta: supportMeta(w.Host, certLabels), Type: corev1.SecretTypeOpaque})
	}
	for _, secret := range empty {
		if _, err := core.Secrets(SupportNamespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil &&
			!apierrors.IsAlreadyExists(err) {
			return supportError("create", "secret", secret.Name, err)
		}
	}
	if err := c.putSupportConfig(ctx, labels, config); err != nil {
		return err
	}

	if err := c.putSupportDeployment(ctx, w, labels); err != nil {
		return err
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: supportBindingName(w.Window), Labels: labels},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: role},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.GroupKind, APIGroup: rbacv1.GroupName, Name: supportGroup}},
	}
	if _, err := c.clientset.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{}); err != nil &&
		!apierrors.IsAlreadyExists(err) {
		return supportError("create", "cluster role binding", binding.Name, err)
	}

	return nil
}

func (c *Client) putSupportConfig(ctx context.Context, labels map[string]string, config []byte) error {
	secrets := c.clientset.CoreV1().Secrets(SupportNamespace)
	secret := &corev1.Secret{
		ObjectMeta: supportMeta(supportConfigName, labels),
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{supportConfigKey: config},
	}
	_, err := secrets.Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = secrets.Update(ctx, secret, metav1.UpdateOptions{})
	}
	if err != nil {
		return supportError("write", "secret", supportConfigName, err)
	}

	return nil
}

func (c *Client) putSupportDeployment(ctx context.Context, w SupportWorkload, labels map[string]string) error {
	deployments := c.clientset.AppsV1().Deployments(SupportNamespace)
	meta := supportMeta(supportProxyName, labels)
	meta.Annotations = map[string]string{supportExpiresAnnotation: w.ExpiresAt.UTC().Format(time.RFC3339)}
	if w.Host != "" {
		meta.Annotations[supportHostAnnotation] = w.Host
	}
	deployment := &appsv1.Deployment{
		ObjectMeta: meta,
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": supportProxyName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					// A new key is a new config, which the proxy reads at
					// start, so each install restarts it.
					Annotations: map[string]string{"pds.percona.com/support-installed-at": time.Now().UTC().Format(time.RFC3339Nano)},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: supportProxyName,
					Containers: []corev1.Container{{
						Name:  "proxy",
						Image: supportProxyImage,
						Env: []corev1.EnvVar{
							{Name: "TS_K8S_PROXY_CONFIG", Value: supportConfigMount + "/" + supportConfigKey},
							{Name: "POD_NAME", ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.name"}}},
							{Name: "POD_UID", ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}},
						},
						VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: supportConfigMount, ReadOnly: true}},
						Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("25m"),
							corev1.ResourceMemory: resource.MustParse("64Mi"),
						}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: ptr.To(false),
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
						},
					}},
					Volumes: []corev1.Volume{{
						Name:         "config",
						VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: supportConfigName}},
					}},
				},
			},
		},
	}
	_, err := deployments.Create(ctx, deployment, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		_, err = deployments.Update(ctx, deployment, metav1.UpdateOptions{})
	}
	if err != nil {
		return supportError("write", "deployment", supportProxyName, err)
	}

	return nil
}

// ExtendSupportWorkload moves the expiry of a window's workload, which is what
// the agent removes it by. It changes nothing else, so the proxy keeps running.
func (c *Client) ExtendSupportWorkload(ctx context.Context, window string, expiresAt time.Time) error {
	if err := c.available(); err != nil {
		return err
	}
	deployments := c.clientset.AppsV1().Deployments(SupportNamespace)
	deployment, err := deployments.Get(ctx, supportProxyName, metav1.GetOptions{})
	if err != nil {
		return supportError("read", "deployment", supportProxyName, err)
	}
	// The fixed names may belong to a newer window by now.
	if deployment.Labels[SupportWindowLabel] != window {
		return supportError("extend", "deployment", supportProxyName, errNotWindowsWorkload)
	}
	if deployment.Annotations == nil {
		deployment.Annotations = map[string]string{}
	}
	deployment.Annotations[supportExpiresAnnotation] = expiresAt.UTC().Format(time.RFC3339)
	if _, err := deployments.Update(ctx, deployment, metav1.UpdateOptions{}); err != nil {
		return supportError("write", "deployment", supportProxyName, err)
	}

	return nil
}

// RemoveSupportWorkload deletes everything a window created, access first. A
// window with nothing left is not an error.
func (c *Client) RemoveSupportWorkload(ctx context.Context, window string) error {
	if err := c.available(); err != nil {
		return err
	}
	selector := SupportWindowLabel + "=" + window

	bindings := c.clientset.RbacV1().ClusterRoleBindings()
	list, err := bindings.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return supportError("list", "cluster role bindings", window, err)
	}
	for _, binding := range list.Items {
		if err := bindings.Delete(ctx, binding.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return supportError("delete", "cluster role binding", binding.Name, err)
		}
	}

	// Only a window's own objects go: a newer window may already be using the
	// fixed names.
	owned, err := c.supportObjectsOwnedBy(ctx, window)
	if err != nil {
		return err
	}
	if owned.deployment {
		err := c.clientset.AppsV1().Deployments(SupportNamespace).Delete(ctx, supportProxyName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return supportError("delete", "deployment", supportProxyName, err)
		}
	}
	for _, name := range owned.secrets {
		err := c.clientset.CoreV1().Secrets(SupportNamespace).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return supportError("delete", "secret", name, err)
		}
	}
	if owned.account {
		err := c.clientset.CoreV1().ServiceAccounts(SupportNamespace).Delete(ctx, supportProxyName, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return supportError("delete", "service account", supportProxyName, err)
		}
	}

	return nil
}

type supportOwned struct {
	deployment, account bool
	secrets             []string
}

func (c *Client) supportObjectsOwnedBy(ctx context.Context, window string) (supportOwned, error) {
	var owned supportOwned
	opts := metav1.ListOptions{LabelSelector: SupportWindowLabel + "=" + window}

	deployments, err := c.clientset.AppsV1().Deployments(SupportNamespace).List(ctx, opts)
	if err != nil {
		return owned, supportError("list", "deployments", window, err)
	}
	owned.deployment = len(deployments.Items) > 0

	accounts, err := c.clientset.CoreV1().ServiceAccounts(SupportNamespace).List(ctx, opts)
	if err != nil {
		return owned, supportError("list", "service accounts", window, err)
	}
	owned.account = len(accounts.Items) > 0

	secrets, err := c.clientset.CoreV1().Secrets(SupportNamespace).List(ctx, opts)
	if err != nil {
		return owned, supportError("list", "secrets", window, err)
	}
	for _, secret := range secrets.Items {
		owned.secrets = append(owned.secrets, secret.Name)
	}

	return owned, nil
}

// SupportWindows lists the windows whose workload is on the environment, which
// is what the agent tells the hub it holds.
func (c *Client) SupportWindows(ctx context.Context) ([]SupportWindow, error) {
	if err := c.available(); err != nil {
		return nil, err
	}
	deployments, err := c.clientset.AppsV1().Deployments(SupportNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: SupportWindowLabel,
	})
	if err != nil {
		return nil, supportError("list", "deployments", SupportNamespace, err)
	}
	out := make([]SupportWindow, 0, len(deployments.Items))
	for _, deployment := range deployments.Items {
		window := SupportWindow{ID: deployment.Labels[SupportWindowLabel], Host: deployment.Annotations[supportHostAnnotation]}
		// A workload with no readable expiry is treated as already over, so a
		// malformed one is removed instead of kept open.
		if at, err := time.Parse(time.RFC3339, deployment.Annotations[supportExpiresAnnotation]); err == nil {
			window.ExpiresAt = at
		}
		out = append(out, window)
	}

	return out, nil
}

// SupportServes says whether a window's proxy can answer a TLS handshake: it
// holds a certificate for the window's address that has not expired, and its
// key. Until the proxy has written one, every handshake to the address times
// out, and issuing it can fail. The certificate outlives windows, so a later
// window can find it expired and wait for the renewal like a first issuance.
// A window with no address has no certificate to wait for.
func (c *Client) SupportServes(ctx context.Context, w SupportWindow) (bool, error) {
	if err := c.available(); err != nil {
		return false, err
	}
	if w.Host == "" {
		return true, nil
	}
	secret, err := c.clientset.CoreV1().Secrets(SupportNamespace).Get(ctx, w.Host, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, supportError("read", "secret", w.Host, err)
	}

	if len(secret.Data[corev1.TLSPrivateKeyKey]) == 0 {
		return false, nil
	}

	return certificateValidAt(secret.Data[corev1.TLSCertKey], time.Now()), nil
}

// certificateValidAt says whether the first certificate in the PEM data is
// valid at now. Data that is not a certificate is no certificate.
func certificateValidAt(data []byte, now time.Time) bool {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}

	return !now.Before(cert.NotBefore) && now.Before(cert.NotAfter)
}

func supportRole(level string) (string, error) {
	switch level {
	case "readOnly":
		return supportReadRole, nil
	case "admin":
		return supportAdminRole, nil
	}

	return "", fmt.Errorf("support access level %q has no role", level)
}

func supportBindingName(window string) string {
	return "pds-support-" + strings.ReplaceAll(strings.TrimPrefix(window, "win_"), "_", "-")
}

// supportConfig is the proxy's config file. The device is named for the window,
// which is how the hub finds it to delete at the end. serviceName makes the
// proxy serve and issue a certificate for the address, but only
// advertiseServices makes the tailnet route the address to this device.
func supportConfig(w SupportWorkload) ([]byte, error) {
	config := map[string]any{
		"version":  "v1alpha1",
		"authKey":  w.Key,
		"hostname": w.Window,
		"state":    "kube:" + supportStateName,
		"apiServerProxy": map[string]any{
			"enabled":     true,
			"mode":        "auth",
			"serviceName": w.Address,
			"issueCerts":  true,
		},
	}
	if w.Address != "" {
		config["advertiseServices"] = []string{w.Address}
	}

	return json.Marshal(config)
}

func supportMeta(name string, labels map[string]string) metav1.ObjectMeta {
	copied := make(map[string]string, len(labels))
	for k, v := range labels {
		copied[k] = v
	}

	return metav1.ObjectMeta{Name: name, Namespace: SupportNamespace, Labels: copied}
}

// supportError keeps the failing call and the object, and never a value: the
// config Secret holds a key.
func supportError(op, kind, name string, err error) error {
	return fmt.Errorf("support access %s %s %q: %w", op, kind, name, err)
}
