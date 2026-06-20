package store

// CommitIdentityArgs are the git -c flags applied to every store commit. They
// stamp aimd's dedicated bot identity as author and committer, and disable
// inherited signing so commits succeed in noninteractive contexts such as CI,
// sandboxes, missing keys, or environments without pinentry.
var CommitIdentityArgs = []string{
	"-c", "user.email=aimd-bot@cybersecauto-labs.org",
	"-c", "user.name=aimd-bot",
	"-c", "commit.gpgsign=false",
}
