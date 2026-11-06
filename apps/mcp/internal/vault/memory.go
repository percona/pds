package vault

import "sync"

// Memory is a Vault that keeps passwords in memory, for tests of code that stores one.
type Memory struct {
	mu sync.Mutex
	// Err, when set, is what every call answers, as a computer with no credential store does.
	Err     error
	secrets map[string]string
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory { return &Memory{secrets: map[string]string{}} }

// Set stores a password.
func (m *Memory) Set(account, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.secrets[account] = password

	return nil
}

// Get returns a stored password.
func (m *Memory) Get(account string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	password, ok := m.secrets[account]
	if !ok {
		return "", ErrNotFound
	}

	return password, nil
}

// Delete removes a stored password.
func (m *Memory) Delete(account string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.secrets, account)

	return nil
}

// Accounts lists the accounts that hold a password.
func (m *Memory) Accounts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for account := range m.secrets {
		out = append(out, account)
	}

	return out
}
