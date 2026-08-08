package models

// PageStatus represents the processing status of a page in the database
type PageStatus string

const (
	PageStatusUnset    PageStatus = ""          // Zero value = unset/unknown
	PageStatusPending  PageStatus = "pending"   // Page queued but not processed
	PageStatusSuccess  PageStatus = "success"   // Page processed successfully
	PageStatusFailure  PageStatus = "failure"   // Page processing failed
	PageStatusNotFound PageStatus = "not_found" // Page not in database
	PageStatusDBError  PageStatus = "db_error"  // Database error occurred
)

// String implements fmt.Stringer for logging
func (s PageStatus) String() string {
	if s == "" {
		return "unset"
	}
	return string(s)
}

// ImageStatus represents the processing status of an image in the database
type ImageStatus string

const (
	ImageStatusUnset    ImageStatus = ""          // Zero value = unset/unknown
	ImageStatusPending  ImageStatus = "pending"   // Image queued for download
	ImageStatusSuccess  ImageStatus = "success"   // Image downloaded successfully
	ImageStatusFailure  ImageStatus = "failure"   // Image download failed
	ImageStatusSkipped  ImageStatus = "skipped"   // Image skipped (size/domain filter)
	ImageStatusNotFound ImageStatus = "not_found" // Image not in database
	ImageStatusDBError  ImageStatus = "db_error"  // Database error occurred
)

// String implements fmt.Stringer for logging
func (s ImageStatus) String() string {
	if s == "" {
		return "unset"
	}
	return string(s)
}
