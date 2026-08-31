package stream

import "encoding/gob"

// eino serializes interrupt Info via gob when writing to the CheckPointStore.
// Every concrete type stashed via tool.Interrupt(ctx, info) has to be
// gob-registered up front, otherwise the checkpoint save fails at runtime
// with a "gob: type not registered" error and resume dies.
func init() {
	gob.Register(&ApprovalInfo{})
	gob.Register(&QuestionInfo{})
	gob.Register(&PlanInfo{})
}
