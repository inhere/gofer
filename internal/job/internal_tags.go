package job

// MessengerJobTag marks the delivery record created when the web console sends
// a message to an existing terminal session. It is an internal implementation
// tag: ordinary job lists hide it, while --all and job details retain it.
const MessengerJobTag = "session-messenger"
