// Package jsonapi is a client for Herdr's JSON API on herdr.sock
// (pkg/herdr/HERDR.md §3). Herdr answers one request per connection and
// closes it, so each Call dials anew; events.subscribe keeps its connection
// open and streams events until it fails or ctx ends. Events are hints to
// reread state: they share no sequence with session.snapshot, so on
// events_lost resubscribe and take a fresh snapshot instead of replaying.
package jsonapi
