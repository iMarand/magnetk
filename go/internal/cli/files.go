package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/iMarand/magnetk/go/pkg/config"
	"github.com/iMarand/magnetk/go/pkg/files"
	"github.com/iMarand/magnetk/go/pkg/magnetk"
	"github.com/iMarand/magnetk/go/pkg/node"
)

// ShareMain shares files and serves them until interrupted.
func ShareMain(args []string) error {
	nf := newNodeFlags("share")
	chunk := nf.fs.String("chunk", "", "chunk size (e.g. 1MB; default automatic)")
	nf.linkRelays = nf.fs.String("link-relay", "", "public relay address(es) to put in links (default: -relay)")
	quiet := nf.fs.Bool("q", false, "print only the links")
	if err := nf.parse(args); err != nil {
		return err
	}
	paths := nf.fs.Args()
	if len(paths) == 0 {
		return fmt.Errorf("usage: magnetk share [flags] <file>...")
	}
	n, err := nf.start(nil)
	if err != nil {
		return err
	}
	defer n.Close()
	return shareAndServe(n, paths, *chunk, *quiet)
}

func shareAndServe(n *node.Node, paths []string, chunk string, quiet bool) error {
	opts := &node.ShareOptions{}
	if chunk != "" {
		cs, err := parseSize(chunk)
		if err != nil {
			return fmt.Errorf("-chunk: %w", err)
		}
		opts.ChunkSize = cs
	}
	info := n.Info()
	if !quiet {
		fmt.Printf("Connected to relay %s (%s) as %s\n", info.RelayID, info.Protocol, info.NodeID)
		if len(info.Direct) > 0 {
			fmt.Printf("Direct connections accepted on %s\n", strings.Join(info.Direct, ", "))
		}
	}
	ctx, cancel := signalContext()
	defer cancel()
	for _, p := range paths {
		start := time.Now()
		lastPrint := time.Time{}
		opts.Progress = func(done, total int64) {
			if quiet || time.Since(lastPrint) < 200*time.Millisecond {
				return
			}
			lastPrint = time.Now()
			fmt.Printf("\rHashing %s: %.0f%%", p, float64(done)*100/float64(max64(total, 1)))
		}
		sf, err := n.Share(ctx, p, opts)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if quiet {
			fmt.Println(sf.Link)
			continue
		}
		fmt.Printf("\r%-60s\n", "")
		fmt.Printf("Shared %s (%s, %d chunks of %s, hashed in %v)\n", sf.Name, humanBytes(float64(sf.Size)),
			(sf.Size+sf.ChunkSize-1)/max64(sf.ChunkSize, 1), humanBytes(float64(sf.ChunkSize)), time.Since(start).Round(time.Millisecond))
		// "Magnetk Link:" is parsed by older SDKs; keep the exact prefix.
		fmt.Printf("  Magnetk Link: %s\n", sf.Link)
	}
	if !quiet {
		fmt.Println("Serving. Press Ctrl+C to stop.")
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	var lastBytes uint64
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			st := n.Stats()
			if !quiet && st.ServedBytes != lastBytes {
				fmt.Printf("[%s] served %s (%d chunks) in total\n", time.Now().Format("15:04:05"), humanBytes(float64(st.ServedBytes)), st.ServedChunks)
				lastBytes = st.ServedBytes
			}
		}
	}
}

// GetMain downloads a file.
func GetMain(args []string) error {
	nf := newNodeFlags("get")
	out := nf.fs.String("o", "", "output file or directory (default: current directory)")
	seed := nf.fs.Bool("seed", false, "keep sharing the file after downloading")
	streams := nf.fs.Int("streams", 0, "parallel streams per provider")
	noResume := nf.fs.Bool("no-resume", false, "ignore partial downloads")
	if err := nf.parse(args); err != nil {
		return err
	}
	if nf.fs.NArg() != 1 {
		return fmt.Errorf("usage: magnetk get [flags] <link|hash>")
	}
	return download(nf, nf.fs.Arg(0), *out, *seed, *streams, *noResume)
}

func download(nf *nodeFlags, linkOrHash, out string, seed bool, streams int, noResume bool) error {
	var linkRelays []string
	if ml, err := magnetk.Parse(linkOrHash); err == nil {
		linkRelays = ml.Relays
	}
	n, err := nf.start(linkRelays)
	if err != nil {
		return err
	}
	defer n.Close()
	ctx, cancel := signalContext()
	defer cancel()

	fmt.Printf("Connected to relay %s via %s\n", n.Info().RelayID, n.Info().Protocol)
	lastLen := 0
	res, err := n.Download(ctx, linkOrHash, out, &node.DownloadOptions{
		Seed:             seed,
		StreamsPerSource: streams,
		NoResume:         noResume,
		Progress: func(p files.Progress) {
			line := fmt.Sprintf("\r%s  %5.1f%%  %s / %s  %s/s  ETA %s  [%d source(s), %d stream(s)]",
				p.Name, p.Percent(), humanBytes(float64(p.Done)), humanBytes(float64(p.Size)),
				humanBytes(p.BytesPerSec), p.ETA.Round(time.Second), p.Sources, p.Streams)
			pad := lastLen - len(line)
			lastLen = len(line)
			if pad > 0 {
				line += strings.Repeat(" ", pad)
			}
			fmt.Print(line)
		},
	})
	fmt.Println()
	if err != nil {
		if ctx.Err() != nil {
			fmt.Println("Interrupted; run the same command again to resume.")
		}
		return err
	}
	secs := res.Elapsed.Seconds()
	fmt.Printf("Downloaded %s (%s in %.2fs, %s/s", res.Path, humanBytes(float64(res.Bytes)), secs, humanBytes(float64(res.Bytes)/maxf(secs, 1e-9)))
	if res.Resumed > 0 {
		fmt.Printf(", %s resumed", humanBytes(float64(res.Resumed)))
	}
	fmt.Println(")")
	if len(res.PerSource) > 1 {
		for src, b := range res.PerSource {
			fmt.Printf("  from %-24s %s\n", src, humanBytes(float64(b)))
		}
	}
	fmt.Printf("Verified SHA-256 %s\n", res.Manifest.Hash)
	if seed {
		fmt.Println("Seeding. Press Ctrl+C to stop.")
		<-ctx.Done()
	}
	return nil
}

func parseSize(s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return config.ParseSize(s)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
