package requesttiming

// QueueDepth is an instantaneous, bounded queue gauge, safe during Drain.
func (e *Enricher) QueueDepth() int {
	if e == nil {
		return 0
	}
	return len(e.ch)
}
