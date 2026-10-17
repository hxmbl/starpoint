//go:build !unix

package privdrop

func ownedDir(string, int) bool { return false }

// OwnedBy reports whether path exists and is owned by uid. Ownership by uid
// does not exist here.
func OwnedBy(string, int) bool { return false }
