package hub

import (
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

type environmentFile struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// LoadInventory registers each clusters/*/cluster.yaml row. Missing files are
// not an error: a heartbeat can still introduce a spoke.
func LoadInventory(root string, inv *Inventory) error {
	if inv == nil {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(root, "clusters", "*", "cluster.yaml"))
	if err != nil {
		return err
	}
	for _, path := range matches {
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var row environmentFile
		if err := yaml.Unmarshal(body, &row); err != nil {
			return err
		}
		name := row.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(path))
		}
		ns := row.Namespace
		if ns == "" {
			ns = "pds"
		}
		inv.Register(name, ns)
	}

	return nil
}
