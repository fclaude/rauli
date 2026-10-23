package chrome

import "context"

// Pages lists the open tabs. A tab a page opened for itself, such as a link
// with target="_blank", is attached the first time it is listed, so it can be
// driven like any other.
func (b *Browser) Pages(ctx context.Context) ([]*Page, error) {
	var res struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := b.conn.Call(ctx, "", "Target.getTargets", nil, &res); err != nil {
		return nil, err
	}
	var out []*Page
	open := map[string]bool{}
	for _, t := range res.TargetInfos {
		if t.Type != "page" {
			continue
		}
		open[t.TargetID] = true
		b.mu.Lock()
		p := b.tabs[t.TargetID]
		b.mu.Unlock()
		if p == nil {
			var err error
			if p, err = b.attach(ctx, t.TargetID); err != nil {
				return nil, err
			}
		}
		out = append(out, p)
	}
	// Forget tabs that have closed.
	b.mu.Lock()
	for id := range b.tabs {
		if !open[id] {
			delete(b.tabs, id)
		}
	}
	b.mu.Unlock()
	return out, nil
}
