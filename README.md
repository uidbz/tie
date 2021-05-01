### tiedb is a key-value-value store, where every value is a key.
### tie is the CLI

The idea is basically to store trains of thought. The premise is that every-thing is an association. The word is the thing. All words are explained by associations to other words. The relation between the associations is an association as well.

That gives us:
thing association relation

thing, association, relation is the same thing which I call an association, because no thing can stand alone; it only makes sense in relation to something else.

So what we need is 2 things: Self/other, full/empty, key/value.
But for convenience, lets throw in a 'relation', which is just another 'value' which is just another 'key', which is just another 'value'.

To initiate a look up, you need a key - so the first value is called 'key'. Association and Relation are just values (and keys), so I call them value1 and value2.
Hence the key-value-value store description.

These 3 go together. I call this a 'tie' - tie associates and binds things together elegantly.

I have later learned that it seems very much like a triple store and a graph database though.


### Features
* Fast, easy and simple (like everything else).
* **Written in Go**: 1 static binary for server. 1 static binary for client.
* Uses concurrency in several places.
* **The daemon forces bi-directional ties.** (circular references)
* Full DB in memory + saves to storage for persistence.
* **Not directly scalable without tricks. Sorry :-(**
* Values are stored in a trie, each trie is in a collection (1 binary file), and collections are in groups, which currently are directories on a file system.
* **It is very suitable and efficient for storing file paths.**
* Identical values in different levels of the trie are referenced in memory, but written to disk.
* tiedb is written for use as a library.
* **tie-daemon exposes a REST API**.
* tie client is written for use as a library.
* **tie contains a file tagger** with file organiser using highway hash.
* tie is a CLI which you can combine with commands like grep, sort and awk.
