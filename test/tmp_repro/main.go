package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/revlist"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"

	"github.com/biox-dev/gobrave/internal/utils"
)

func main() {
	client.InstallProtocol("file", server.NewClient(server.DefaultLoader))

	target := os.Args[1]
	srcRepoPath := os.Args[2]

	srcRepo, err := git.PlainOpen(srcRepoPath)
	if err != nil {
		fmt.Printf("open src: %v\n", err)
		return
	}
	targetRepo, err := git.PlainOpen(target)
	if err != nil {
		fmt.Printf("open target: %v\n", err)
		return
	}

	fmt.Println("=== target refs vs src objects ===")
	var missing []plumbing.Hash
	iter, err := targetRepo.References()
	if err != nil {
		fmt.Printf("refs: %v\n", err)
		return
	}
	for {
		ref, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			fmt.Printf("refs next: %v\n", err)
			return
		}
		if ref.Type() != plumbing.HashReference {
			continue
		}
		state := "in-src"
		if obErr := srcRepo.Storer.HasEncodedObject(ref.Hash()); errors.Is(obErr, plumbing.ErrObjectNotFound) {
			state = "*** MISSING IN SRC ***"
			missing = append(missing, ref.Hash())
		} else if obErr != nil {
			state = fmt.Sprintf("check error: %v", obErr)
		}
		fmt.Printf("    %-40s %s %s\n", ref.Name(), ref.Hash(), state)
	}

	// Simulate what the in-process file server does with the negotiation haves:
	// revlist the haves against the *server side* storer.
	ep, _ := transport.NewEndpoint(srcRepoPath)
	sto, err := server.DefaultLoader.Load(ep)
	if err != nil {
		fmt.Printf("load storer: %v\n", err)
		return
	}
	if len(missing) > 0 {
		_, rerr := revlist.Objects(sto, missing, nil)
		fmt.Printf("[server-side revlist(haves)] err=%v isErrObjectNotFound=%v\n",
			rerr, errors.Is(rerr, plumbing.ErrObjectNotFound))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fmt.Println("=== utils.SyncWorktreeFromRepo ===")
	if err := utils.SyncWorktreeFromRepo(ctx, target, srcRepoPath); err != nil {
		fmt.Printf("[SyncWorktreeFromRepo] FAILED: %v\n", err)
	} else {
		fmt.Println("[SyncWorktreeFromRepo] ok")
	}
}
