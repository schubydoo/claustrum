//go:build !linux && !darwin

package supervisor

// AwaitRecordsRemoved and ForgetKeptRecords are no-ops on windows and other non-linux,
// non-darwin targets: no record exists (internal/childrec/childrecord_other.go), so
// there is nothing to wait for and nothing to remove at shutdown.
func (m *Manager) AwaitRecordsRemoved() {}

func (m *Manager) ForgetKeptRecords() {}
