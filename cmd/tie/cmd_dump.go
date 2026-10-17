package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/uidbz/tie/client"
	"github.com/uidbz/tie/metadata"
	"github.com/uidbz/tie/tiedb"
	"github.com/urfave/cli/v3"
	"golang.org/x/term"
)

func cmdDump(a *app) *cli.Command {
	return &cli.Command{
		Name:  "dump",
		Usage: "Dump every triple in the current collection as TSV (key<TAB>value1<TAB>value2) to stdout",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "file",
				Aliases: []string{"f"},
				Usage:   "Read triples directly from a local .tie file instead of the server (offline export; do not use against a file a running server has open)",
			},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			// A dump is a stream: it never holds the collection, so the table
			// format (which has to see every row to size its columns) is not
			// available here — TSV is the point of the command.
			format, err := outputFormat(ctx)
			if err != nil {
				return err
			}
			asJSON := format == "json"
			if format == "table" {
				format = "tsv"
			}

			bw := bufio.NewWriter(os.Stdout)
			defer bw.Flush()
			first := true
			write := func(t tiedb.StringTriple) error {
				if !asJSON {
					_, err := fmt.Fprintln(bw, client.TSVRecord(t.Key, t.Value1, t.Value2))
					return err
				}
				if !first {
					if _, err := fmt.Fprint(bw, ","); err != nil {
						return err
					}
				}
				first = false
				rec, err := json.Marshal(map[string]string{"key": t.Key, "value1": t.Value1, "value2": t.Value2})
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(bw, "\n%s", rec)
				return err
			}
			if asJSON {
				if _, err := fmt.Fprint(bw, "["); err != nil {
					return operationalError("dump: %v", err)
				}
			}

			if path := ctx.String("file"); path != "" {
				if err := dumpLocalFile(path, write); err != nil {
					return operationalError("dump: %v", err)
				}
			} else {
				if err := a.tie.DumpStream(write); err != nil {
					return operationalError("dump: %v", err)
				}
			}
			if asJSON {
				if _, err := fmt.Fprint(bw, "\n]\n"); err != nil {
					return operationalError("dump: %v", err)
				}
			}
			return bw.Flush()
		},
	}
}

// dumpLocalFile reads every forward triple straight from an on-disk .tie file,
// bypassing the server, and streams each as a TSV record to w (no in-memory
// buffering of the whole collection). It reflects flushed on-disk state only,
// so it is meant for offline export when no server holds the file open.
func dumpLocalFile(path string, write func(tiedb.StringTriple) error) error {
	if !strings.HasSuffix(path, ".tie") {
		return fmt.Errorf("not a .tie file: %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	name := strings.TrimSuffix(filepath.Base(path), ".tie")
	db := tiedb.NewDB(true)
	db.SetBlobPolicy(tiedb.HexHashBlobPolicy(metadata.IsHexHash))
	col := db.GetCollection(tiedb.CollectionKey{Database: dir, Collection: name})
	var writeErr error
	col.ForEachTriple(func(t tiedb.StringTriple) {
		if writeErr != nil {
			return
		}
		writeErr = write(t)
	})
	return writeErr
}

func cmdRestore(a *app) *cli.Command {
	return &cli.Command{
		Name:      "restore",
		Usage:     "Restore triples from a TSV file (key<TAB>value1<TAB>value2) into the current collection (additive); reads stdin if no file is given",
		ArgsUsage: "[file]",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "drop",
				Usage: "Drop the existing collection first, overwriting it entirely with the restored data (default: merge)",
			},
		},
		Action: func(_ context.Context, ctx *cli.Command) error {
			drop := ctx.Bool("drop")
			var src io.Reader = os.Stdin
			var seekable io.Seeker
			if path := ctx.Args().Get(0); path != "" {
				f, err := os.Open(path)
				if err != nil {
					return operationalError("opening input: %v", err)
				}
				defer f.Close()
				src, seekable = f, f
			} else if drop {
				// --drop from stdin has to be spooled: the restore streams, so
				// a malformed line would otherwise surface after the
				// collection was dropped.
				f, err := os.CreateTemp("", "tie-restore-")
				if err != nil {
					return operationalError("spooling input: %v", err)
				}
				defer os.Remove(f.Name())
				defer f.Close()
				if _, err := io.Copy(f, os.Stdin); err != nil {
					return operationalError("reading input: %v", err)
				}
				if _, err := f.Seek(0, io.SeekStart); err != nil {
					return operationalError("reading input: %v", err)
				}
				src, seekable = f, f
			}

			if drop {
				// Nothing is destroyed until the whole input parses. The file
				// is read twice rather than held in memory (§4.18).
				n, err := validateTSV(src)
				if err != nil {
					return fmt.Errorf("input rejected before dropping anything: %w (checked %d lines)", err, n)
				}
				if seekable == nil {
					return exitError{code: exitUsage, msg: "--drop needs a seekable input: pass a file, or a pipe you can rewind"}
				}
				if _, err := seekable.Seek(0, io.SeekStart); err != nil {
					return operationalError("reading input: %v", err)
				}
				if err := a.tie.DropCollection(); err != nil {
					return operationalError("dropping the collection: %v", err)
				}
			}

			// Chunks of 5 000 ops, each followed by a Sync: a 2M-triple dump is
			// neither a memory problem nor one request, and an interrupted
			// restore keeps what it already applied.
			tty := term.IsTerminal(int(os.Stderr.Fd()))
			total, err := a.tie.RestoreStream(src, func(n int) {
				if tty {
					fmt.Fprintf(os.Stderr, "\rRestored %d triples", n)
				}
			})
			if err != nil {
				if tty {
					fmt.Fprint(os.Stderr, "\r")
				}
				return operationalError("restore failed after %d triples: %v", total, err)
			}
			if err := a.tie.Sync(); err != nil {
				return operationalError("sync: %v", err)
			}
			if tty {
				fmt.Fprintf(os.Stderr, "\rRestored %d triples\n", total)
			} else {
				fmt.Fprintf(os.Stderr, "Restored %d triples\n", total)
			}
			return nil
		},
	}
}

// validateTSV parses a TSV dump without keeping it, and reports how many lines
// it checked. It is the pre-flight pass that lets --drop refuse a malformed
// input instead of destroying the collection first.
func validateTSV(src io.Reader) (int, error) {
	r := bufio.NewReader(src)
	lines := 0
	for {
		text, err := r.ReadString('\n')
		if text != "" {
			lines++
			if _, perr := client.SplitTSVRecord(strings.TrimRight(text, "\r\n"), 3); perr != nil {
				return lines, fmt.Errorf("malformed line %d: %w", lines, perr)
			}
		}
		if err != nil {
			if err == io.EOF {
				return lines, nil
			}
			return lines, err
		}
	}
}
