package model

// RunAs is the account a provider's commands ran as (or, in a plan, will run
// as) for one action. Elevated runs make it matter: under sudo or doas from an
// ordinary user, user-level providers run as that user and system providers
// as root.
type RunAs struct {
	// Provider is the provider whose commands this describes.
	Provider string `json:"provider"`
	// User is the account name; UID and GID its ids. UID is -1 on Windows,
	// which has no numeric user ids.
	User string `json:"user"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	// Elevated is true when Starpoint itself ran as root or elevated.
	Elevated bool `json:"elevated"`
	// Dropped is true when Starpoint, running as root, started the provider's
	// commands as the invoking user instead.
	Dropped bool `json:"dropped"`
	// Privilege is the provider's classification under an elevated run with
	// an invoking user: "user" or "root". Empty otherwise.
	Privilege string `json:"privilege,omitempty"`
	// Reason says why, in one sentence.
	Reason string `json:"reason,omitempty"`
}
