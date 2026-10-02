package update

import "context"

type AsyncCheck struct {
	checker *Checker
	result  <-chan *Notice
}

func Start(ctx context.Context, checker *Checker) *AsyncCheck {
	result := make(chan *Notice, 1)
	go func() {
		notice, _ := checker.Check(ctx)
		result <- notice
	}()
	return &AsyncCheck{checker: checker, result: result}
}

func (c *AsyncCheck) Result() *Notice {
	if c == nil {
		return nil
	}
	select {
	case notice := <-c.result:
		return notice
	default:
		return nil
	}
}

func (c *AsyncCheck) MarkNotified(notice Notice) {
	if c != nil {
		_ = c.checker.MarkNotified(notice)
	}
}
