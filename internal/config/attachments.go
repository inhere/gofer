package config

import (
	"fmt"
	"time"
)

// AttachmentsConfig is the server.attachments block: the numbers of the workbench image
// attachments (design 2026-10-19-workbench-image-attachments-design.md 「大小与数量」). Every
// key is optional; 0 / unset takes the documented default and a negative value is a load error.
// The upload handler reads them per request, so an edit applies to the next upload.
type AttachmentsConfig struct {
	// MaxBytes caps ONE uploaded image (0 => 10 MiB; upper bound attachmentsMaxBytesLimit,
	// because an upload is held in memory while it is validated).
	MaxBytes int64 `yaml:"max_bytes,omitempty"`
	// MaxPerMessage is how many attachments one message may carry (0 => 6). The upload route
	// does not enforce it — the send path of the workbench does.
	MaxPerMessage int `yaml:"max_per_message,omitempty"`
	// HourlyPerCaller caps the uploads one caller may attempt per rolling hour (0 => 200).
	HourlyPerCaller int `yaml:"hourly_per_caller,omitempty"`
}

// Attachment defaults.
const (
	DefaultAttachmentMaxBytes        int64 = 10 << 20
	DefaultAttachmentMaxPerMessage         = 6
	DefaultAttachmentHourlyPerCaller       = 200
	// DefaultAttachmentRetentionDays is storage.attachment_retention_days when unset.
	DefaultAttachmentRetentionDays = 14

	attachmentsMaxBytesLimit int64 = 100 << 20
)

// AttachmentSettings is AttachmentsConfig with the defaults applied.
type AttachmentSettings struct {
	MaxBytes        int64
	MaxPerMessage   int
	HourlyPerCaller int
}

// EffectiveAttachments resolves server.attachments over the defaults.
func (c *Config) EffectiveAttachments() AttachmentSettings {
	var a AttachmentsConfig
	if c != nil {
		a = c.Server.Attachments
	}
	out := AttachmentSettings{MaxBytes: a.MaxBytes, MaxPerMessage: a.MaxPerMessage, HourlyPerCaller: a.HourlyPerCaller}
	if out.MaxBytes <= 0 {
		out.MaxBytes = DefaultAttachmentMaxBytes
	}
	if out.MaxPerMessage <= 0 {
		out.MaxPerMessage = DefaultAttachmentMaxPerMessage
	}
	if out.HourlyPerCaller <= 0 {
		out.HourlyPerCaller = DefaultAttachmentHourlyPerCaller
	}
	return out
}

// validate rejects negative numbers and an upload ceiling that would not fit in memory.
func (a AttachmentsConfig) validate() error {
	if a.MaxBytes < 0 {
		return fmt.Errorf("server.attachments.max_bytes must be >= 0 (0 = default)")
	}
	if a.MaxBytes > attachmentsMaxBytesLimit {
		return fmt.Errorf("server.attachments.max_bytes must be <= %d", attachmentsMaxBytesLimit)
	}
	if a.MaxPerMessage < 0 {
		return fmt.Errorf("server.attachments.max_per_message must be >= 0 (0 = default)")
	}
	if a.HourlyPerCaller < 0 {
		return fmt.Errorf("server.attachments.hourly_per_caller must be >= 0 (0 = default)")
	}
	return nil
}

// AttachmentRetention converts AttachmentRetentionDays into the retention window: 0 takes the
// default (14 days), a negative value returns 0 (keep forever).
func (s StorageConfig) AttachmentRetention() time.Duration {
	days := s.AttachmentRetentionDays
	if days == 0 {
		days = DefaultAttachmentRetentionDays
	}
	if days < 0 {
		return 0
	}
	return time.Duration(days) * 24 * time.Hour
}
