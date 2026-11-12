package domain

// Routing keys of avatar events.
const (
	RoutingUploaded = "avatar.uploaded"
	RoutingProcess  = "avatar.process"
	RoutingDeleted  = "avatar.deleted"
)

// AvatarUploadEvent is published after the original reaches object storage.
type AvatarUploadEvent struct {
	AvatarID string `json:"avatar_id"`
	UserID   string `json:"user_id"`
	S3Key    string `json:"s3_key"`
}

// OpResize is the only processing operation: a square thumbnail.
const OpResize = "resize"

// ProcessingOp is one step the worker applies to an avatar.
type ProcessingOp struct {
	Type string `json:"type"`
	Size Size   `json:"size"`
}

// DefaultOperations are applied to every freshly uploaded avatar.
func DefaultOperations() []ProcessingOp {
	ops := make([]ProcessingOp, 0, len(ThumbnailSizes))
	for _, s := range ThumbnailSizes {
		ops = append(ops, ProcessingOp{Type: OpResize, Size: s})
	}
	return ops
}

// AvatarProcessEvent asks the worker to (re)generate thumbnails.
type AvatarProcessEvent struct {
	AvatarID   string         `json:"avatar_id"`
	Operations []ProcessingOp `json:"operations"`
}

// AvatarDeleteEvent asks the worker to remove the objects of a deleted avatar.
type AvatarDeleteEvent struct {
	AvatarID string   `json:"avatar_id"`
	S3Keys   []string `json:"s3_keys"`
}

// Event is an outgoing broker message. It is saved to the outbox in the same transaction as
// the change it reports and published afterwards; ID becomes the message id used for deduplication.
type Event struct {
	ID         string
	RoutingKey string
	Payload    []byte
}
