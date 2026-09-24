package app

// ReleaseSignedMessage is what a release signature covers: the release's tag,
// then its checksum list.
//
// It covered the checksum list alone, and the list names archives, not a
// version — backpack_linux_amd64.tar.gz is the same name in every release. So a
// mirror, or a proxy on the path, could serve an older release's archive,
// checksums and signature under a newer tag, every one of them genuine, and an
// updater would verify it and install it: a downgrade to whatever the older
// release was vulnerable to, carried out with the publisher's own signature.
// Users on restricted networks fetch releases through exactly such proxies.
//
// With the tag inside what is signed, a signature is good for one tag only.
// No release was ever published with the older form: the key and this format
// arrive in the same version.
func ReleaseSignedMessage(tag string, sums []byte) []byte {
	msg := make([]byte, 0, len("backpack release \n")+len(tag)+len(sums))
	msg = append(msg, "backpack release "...)
	msg = append(msg, tag...)
	msg = append(msg, '\n')
	return append(msg, sums...)
}
