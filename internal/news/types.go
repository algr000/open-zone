package news

// Data is the template model for the News endpoint.
// Keep it stable: the in-game renderer expects plain text.
type Data struct {
	Tagline    string
	CreatedBy  string
	Version    string
	ServerTime string

	PlayersOnline int
	GamesHosted   int

	// Optional extra lines appended after the status block.
	Message string
}
