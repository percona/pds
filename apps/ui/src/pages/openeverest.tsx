import { CircleAlert } from "lucide-react";
import { Link } from "react-router";
import { PageHeader } from "@/components/layout/page-header";
import { Skeleton } from "@/components/ui/skeleton";
import {
  CommandBlock,
  GuideRefusal,
  Mono,
  Step,
  awsCleanupPath,
  useGuideTarget,
} from "@/features/environments";

// A terminal that skipped step 1 still has the customer's default kubeconfig,
// so every block that changes the environment first checks that kubectl is on the
// context step 1 saved. It compares for equality: searching for the environment
// name would accept "production" for an environment called "prod".
const onThisEnvironment = '[ "$(kubectl config current-context)" = "${CONTEXT:?}" ]';

/**
 * How to keep a disconnected environment's databases and run them with OpenEverest.
 * Disconnecting alone leaves the control plane's Git applying the databases it
 * last knew, so every step here exists to stop something that would otherwise
 * undo the hand-over. It renders from the route alone because the environment may
 * already be gone from the environments list.
 */
export function OpenEverestPage() {
  const { name, namespace, region, regionUnknown, pending, problem } = useGuideTarget();

  if (problem) {
    return <GuideRefusal title="Continue with OpenEverest" problem={problem} />;
  }

  // Until the default region is known the commands would carry a fallback, and
  // a same-named environment there would be the one step 1 saves and then trusts.
  if (pending) {
    return (
      <div className="space-y-6" aria-busy="true">
        <PageHeader title="Continue with OpenEverest" />
        <Skeleton className="h-48 w-full" />
        <Skeleton className="h-32 w-full" />
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <PageHeader
        title="Continue with OpenEverest"
        description={`Keep the databases on ${name} and run them with OpenEverest yourself. Do these steps in order, then remove the environment from Percona Database Service.`}
      />

      <Step n={1} title="Set the variables">
        <p className="text-sm text-muted-foreground">
          Every command below reads these, so run this block again in any new terminal. Change{" "}
          <Mono>REGION</Mono> if the EKS cluster was created in another region,{" "}
          <Mono>NAMESPACE</Mono> if its databases run somewhere else, and <Mono>RELEASE</Mono> if
          the platform release is not named <Mono>pds</Mono>. The two kubeconfig lines give these
          commands a kubeconfig of their own, and the last two lines save its context and print it.
          Every command that changes the Kubernetes cluster checks that kubectl is still on the
          saved context, so a terminal that skipped this step stops instead of changing another
          Kubernetes cluster.
        </p>
        {regionUnknown ? (
          <p className="flex items-center gap-2 text-sm">
            <CircleAlert className="size-4 text-warning" strokeWidth={1.5} aria-hidden="true" />
            <span>
              The default region could not be read. Set <Mono>REGION</Mono> to the region the EKS
              environment was created in.
            </span>
          </p>
        ) : null}
        <CommandBlock
          command={[
            `export CLUSTER=${name}`,
            `export NAMESPACE=${namespace}`,
            `export REGION=${region}`,
            "export RELEASE=pds",
            'export KUBECONFIG="$(mktemp)"',
            'aws eks update-kubeconfig --name "$CLUSTER" --region "$REGION"',
            'export CONTEXT="$(kubectl config current-context)"',
            'echo "$CONTEXT" | grep -F "$CLUSTER"',
          ].join("\n")}
        />
        <p className="text-sm text-muted-foreground">
          The last command prints the context kubectl uses. If it prints nothing, kubectl is not
          pointed at this Kubernetes cluster, so do not go on. On a Kubernetes cluster that is not
          EKS, skip the <Mono>aws</Mono> command and copy that Kubernetes cluster&apos;s kubeconfig
          to <Mono>$KUBECONFIG</Mono> instead.
        </p>
      </Step>

      <Step n={2} title="Check the OpenEverest version">
        <p className="text-sm text-muted-foreground">
          OpenEverest is what runs your databases, and it becomes yours to run. Read which version
          this Kubernetes cluster has.
        </p>
        <CommandBlock
          command={
            'kubectl -n everest-system get configmap "${RELEASE}-platform-versions" -o jsonpath=\'{.data.openeverest\\.version}{"\\n"}\''
          }
        />
        <p className="text-sm text-muted-foreground">
          PDS pins a prerelease build of OpenEverest, not a stable release. Once the environment
          leaves PDS nothing upgrades it for you, so every upgrade from here is yours to choose,
          test and run.
        </p>
      </Step>

      <Step n={3} title="Detach from the control plane's Git">
        <p className="text-sm text-muted-foreground">
          The Kubernetes cluster applies the last revision of its databases from the control
          plane&apos;s Git every 15 seconds. Until that stops, a change you make in OpenEverest to a
          database PDS created is reverted, and a database you delete there comes back. The first
          block stops the Kubernetes cluster deleting what it applied, then deletes the object that
          applies it. Each command runs only if the one before it worked, because deleting an object
          that still deletes what it applied would delete your databases.
        </p>
        <CommandBlock
          command={[
            `${onThisEnvironment} &&`,
            `kubectl -n flux-system patch kustomization instances --type merge -p '{"spec":{"prune":false}}' &&`,
            "kubectl -n flux-system delete kustomization instances",
          ].join("\n")}
        />
        <p className="text-sm text-muted-foreground">
          If the first block stops because that object is already gone, go on. A Kubernetes cluster
          with an amd64 node has a second object of the same kind that applies its monitoring from
          Git. The second block detaches it the same way, so what it installed stays. If it stops at
          its first command because the Kubernetes cluster has no monitoring from Git, go on.
        </p>
        <CommandBlock
          command={[
            `${onThisEnvironment} &&`,
            `kubectl -n flux-system patch kustomization platform --type merge -p '{"spec":{"prune":false}}' &&`,
            "kubectl -n flux-system delete kustomization platform",
          ].join("\n")}
        />
        <p className="text-sm text-muted-foreground">
          The third block removes what reads from Git and what it was allowed to do. The controllers
          stay, and so do your databases.
        </p>
        <CommandBlock
          command={[
            `${onThisEnvironment} &&`,
            "kubectl -n flux-system delete gitrepository hub --ignore-not-found &&",
            "kubectl -n flux-system delete secret hub-github --ignore-not-found &&",
            "kubectl -n flux-system delete serviceaccount hub-instances --ignore-not-found &&",
            "kubectl -n flux-system delete serviceaccount hub-platform --ignore-not-found &&",
            'kubectl delete rolebinding,role hub-instances -n "$NAMESPACE" --ignore-not-found &&',
            "kubectl delete rolebinding,role hub-platform -n everest-system --ignore-not-found",
          ].join("\n")}
        />
      </Step>

      <Step n={4} title="Stop the agent">
        <p className="text-sm text-muted-foreground">
          The agent reports this environment to the control plane. Removing the environment revokes
          its token, and the agent would go on trying with it. Scale it to zero.
        </p>
        <CommandBlock
          command={[
            `${onThisEnvironment} &&`,
            'kubectl -n everest-system scale deploy "${RELEASE}-adapter" --replicas=0 &&',
            'legacy="$(kubectl get deploy pds-agent -n pds-agent -o name --ignore-not-found)" &&',
            '{ [ -z "$legacy" ] || kubectl scale -n pds-agent --replicas=0 "$legacy"; }',
          ].join("\n")}
        />
        <p className="text-sm text-muted-foreground">
          An environment joined by an older script also runs an agent of its own in the{" "}
          <Mono>pds-agent</Mono> namespace. The last command stops that one too, and does nothing
          when it is not there.
        </p>
        <p className="text-sm text-muted-foreground">
          An upgrade of the platform release with Helm starts the agent again and creates its token
          Secret again, so repeat this step and the token deletion in step 5 after one.
        </p>
      </Step>

      <Step n={5} title="Open OpenEverest">
        <p className="text-sm text-muted-foreground">
          OpenEverest serves its UI inside the Kubernetes cluster only. Forward its port, then open{" "}
          <Mono>http://localhost:8080</Mono> in a browser. The command keeps running, so leave it
          and use a second terminal for the rest of this step, running step 1 there first.
        </p>
        <CommandBlock command="kubectl -n everest-system port-forward svc/everest 8080:8080" />
        <p className="text-sm text-muted-foreground">
          Sign in as <Mono>admin</Mono>. This reads the initial password from the Kubernetes
          environment. Change it the first time you sign in, because until then the password in the
          Secret is the working one.
        </p>
        <CommandBlock
          command={
            "kubectl -n everest-system get secret everest-accounts -o jsonpath='{.data.users\\.yaml}' | base64 -d | awk '$1 == \"passwordHash:\" { print $2; exit }'"
          }
        />
        <p className="text-sm text-muted-foreground">
          The agent signed in with a token it kept in a Secret of its own. Delete the tokens so
          nothing else holds a session you did not open.
        </p>
        <CommandBlock
          command={[
            `${onThisEnvironment} &&`,
            'kubectl delete secret "${RELEASE}-everest" -n everest-system --ignore-not-found &&',
            "kubectl delete secret pds-everest -n pds-agent --ignore-not-found",
          ].join("\n")}
        />
      </Step>

      <Step n={6} title="Keep the backup user and bucket">
        <p className="text-sm text-muted-foreground">
          Backups still write to the backup bucket with the key of the backup IAM user{" "}
          <Mono>pds-{name}</Mono>. The bucket is <Mono>pds-&lt;account-id&gt;-{name}</Mono>, or a
          shortened name that ends in a hash for a long EKS cluster name. A join with{" "}
          <Mono>--local</Mono> made neither. Keep both, and do not run the delete command for as
          long as the databases run.
        </p>
      </Step>

      <Step n={7} title="Remove the environment from PDS">
        <p className="text-sm text-muted-foreground">
          Open Environments, choose Disconnect on <Mono>{name}</Mono>, and click Remove. The
          Kubernetes cluster, its databases and OpenEverest keep running.
        </p>
      </Step>

      <Step title="What changes">
        <p className="text-sm text-muted-foreground">
          Once it is removed, {name} leaves PDS. It gets no changes, monitoring or support cases
          through this console, and upgrades are yours. This guide does not change your existing
          Percona support agreement. Contact Percona support to find out what it covers for a
          Kubernetes cluster you run yourself.
        </p>
        <p className="text-sm text-muted-foreground">
          To delete everything later, use the delete command. Take care: it deletes the databases,
          the backup user and the backup bucket that this hand-over keeps.
        </p>
        <Link
          to={awsCleanupPath(name, namespace)}
          className="rounded-md text-sm font-medium text-primary hover:underline focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none"
        >
          View delete command
        </Link>
      </Step>
    </div>
  );
}
