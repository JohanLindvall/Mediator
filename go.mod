module github.com/JohanLindvall/Mediator

go 1.25.0

require (
	github.com/dhowden/tag v0.0.0-20240417053706-3d75831295e8
	github.com/fsnotify/fsnotify v1.10.1
	go.etcd.io/bbolt v1.5.0
	golang.org/x/image v0.45.0
)

require (
	github.com/klauspost/compress v1.20.1
	github.com/nwaples/rardecode/v2 v2.4.1
	golang.org/x/sys v0.47.0
)

// rardecode v2.4.1 drops the rest of an LZ copy that runs past the end of its
// window, so a member with such a copy decodes short and fails its checksum —
// measured on two RAR 2.9 films here, wrong from the third window wrap on.
// Pinned to upstream's v2.4.1 plus the two commits of nwaples/rardecode#69,
// which fix that and were checked against RAR's own extraction byte for byte;
// drop this once a release of rardecode carries them.
replace github.com/nwaples/rardecode/v2 => github.com/unxed/rardecode/v2 v2.0.0-20260925135740-45ace946a63b
