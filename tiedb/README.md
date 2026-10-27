# tiedb

Golang package to associate and relate bytes/strings using a trie and red-black-trees.

See [../docs/internals.md](../docs/internals.md) for the data structures, on-disk
file format, and memory model (including the opt-in reverse-association index).
Free-text search (`Collection.Search`) is a transient, generation-invalidated
view over the same index; see [../docs/search.md](../docs/search.md).