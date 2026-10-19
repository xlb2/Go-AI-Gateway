package session

import "github.com/cloudwego/eino/schema"

// ProjectedSource refers to an event in the supplied log snapshot, not a model
// input position. Seq alone is not a durable ID: owner/generation must be bound
// by a storage snapshot before this can be exposed as a readback reference.
type ProjectedSource struct {
	Message     *schema.Message
	Seq         int
	EventType   string
	Interrupted bool
	Summary     bool
	CoveredFrom int
	CoveredTo   int
}

// ProjectMessagesWithSources shares the production projection implementation.
// A summary points to its own event, with covered range separately recorded;
// it never claims that its generated text is a quote of the covered events.
// This does not perform token trimming or re-read Redis.
func ProjectMessagesWithSources(dtos []MemoryDTO, baseSeq int) []ProjectedSource {
	result := make([]ProjectedSource, 0, len(dtos))
	projectMessagesFrom(dtos, baseSeq, func(msg *schema.Message, index int) {
		dto := dtos[index]
		typ := dto.Type
		if typ == "" {
			typ = inferEventType(schema.RoleType(dto.Role))
		}
		p := ProjectedSource{Message: msg, Seq: baseSeq + index, EventType: typ, Interrupted: dto.Interrupted,
			Summary: typ == EventCompactionSummary, CoveredFrom: -1, CoveredTo: -1}
		if p.Summary {
			p.CoveredFrom = dto.CompactionFrom
			p.CoveredTo = dto.CompactionTo
		}
		result = append(result, p)
	})
	return result
}
