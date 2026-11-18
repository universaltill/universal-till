package offsitebackup

// Wait blocks until the running restore ends (tests only: production code uses
// WaitTimeout).
func (r *Restorer) Wait() {
	r.mu.Lock()
	d := r.done
	r.mu.Unlock()
	if d != nil {
		<-d
	}
}
