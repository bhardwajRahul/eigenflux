package skills

import "fmt"

// ValidateSignedRelease uses the client trust root and also checks that the
// content revision is derived from the signed skill hashes.
func ValidateSignedRelease(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("nil release manifest")
	}
	if err := validateRemoteManifest(m); err != nil {
		return err
	}
	if err := verifyManifestSignature(m); err != nil {
		return err
	}
	if m.Revision != computeRevision(m.Skills) {
		return fmt.Errorf("release revision does not match skill hashes")
	}
	return nil
}
