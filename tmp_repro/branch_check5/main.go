package main

// 临时验证：
// 1) wt.Reset(HardReset) 会不会改变目标目录的分支名？
// 2) push 用 refspec 改名（源 master -> store 的 main）+ 把 store HEAD 指到 main，是否可行？

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/biox-dev/gobrave/internal/utils"
)

func headOf(dir string) string {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return fmt.Sprintf("<open err: %v>", err)
	}
	head, err := repo.Head()
	if err != nil {
		return fmt.Sprintf("<Head err: %v>", err)
	}
	return fmt.Sprintf("%s @ %s", head.Name().Short(), head.Hash().String()[:7])
}

func writeAndCommit(dir, content, msg string, identity utils.GitIdentity) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		panic(err)
	}
	os.WriteFile(filepath.Join(dir, "script.json"), []byte(content), 0o644)
	if _, err := utils.CommitAll(repo, msg, identity); err != nil {
		panic(err)
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	identity := utils.GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}

	root, err := os.MkdirTemp("", "branch_check5_")
	if err != nil {
		panic(err)
	}
	fmt.Println("root:", root)

	// 源目录：master 分支
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	srepo, err := git.PlainInitWithOptions(src, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("master")},
	})
	if err != nil {
		panic(err)
	}
	_ = srepo
	writeAndCommit(src, `{"v":1}`, "v1", identity)
	fmt.Println("src HEAD:", headOf(src))

	store := filepath.Join(root, "store")
	if _, err := utils.EnsureBareGitRepo(store); err != nil {
		panic(err)
	}

	// --- 实验 1：改名 refspec + 对齐 store HEAD ---
	fmt.Println("\n=== 实验1: push master -> store 的 main，并把 store HEAD 指到 main ===")
	srcRepo, err := git.PlainOpen(src)
	if err != nil {
		panic(err)
	}
	head, err := srcRepo.Head()
	if err != nil {
		panic(err)
	}
	if _, err := srcRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{store}}); err != nil {
		panic(err)
	}
	refSpec := gitconfig.RefSpec(fmt.Sprintf("+%s:refs/heads/main", head.Name()))
	if err := srcRepo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", RefSpecs: []gitconfig.RefSpec{refSpec}}); err != nil {
		fmt.Println("push err:", err)
	}
	storeRepo, err := git.PlainOpen(store)
	if err != nil {
		panic(err)
	}
	if err := storeRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		panic(err)
	}
	fmt.Println("store HEAD:", headOf(store))
	if rel, err := utils.FindFileInGitRepo(store, "script.json"); err != nil {
		fmt.Println("FindFileInGitRepo err:", err)
	} else if content, err := utils.ReadFileFromGitRepo(store, rel); err != nil {
		fmt.Println("ReadFileFromGitRepo err:", err)
	} else {
		fmt.Println("store 读到:", string(content))
	}

	// 再发布一次（v2），验证 store HEAD 一直有效、内容跟着更新
	writeAndCommit(src, `{"v":2}`, "v2", identity)
	srcRepo, _ = git.PlainOpen(src)
	head, _ = srcRepo.Head()
	if err := srcRepo.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec(fmt.Sprintf("+%s:refs/heads/main", head.Name()))},
	}); err != nil {
		fmt.Println("push#2 err:", err)
	}
	fmt.Println("store HEAD after publish#2:", headOf(store))
	if content, err := utils.ReadFileFromGitRepo(store, "script.json"); err != nil {
		fmt.Println("read err:", err)
	} else {
		fmt.Println("store 读到:", string(content))
	}

	// --- 实验 2：reset 会不会改变目标目录的分支名 ---
	fmt.Println("\n=== 实验2: 目标目录是 main 分支，reset 到 store 的提交后分支名是否变化 ===")
	target := filepath.Join(root, "install", "s1")
	os.MkdirAll(target, 0o755)
	trepo, err := git.PlainInitWithOptions(target, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		panic(err)
	}
	_ = trepo
	os.WriteFile(filepath.Join(target, "old.txt"), []byte("old\n"), 0o644)
	tRepo, _ := git.PlainOpen(target)
	if _, err := utils.CommitAll(tRepo, "old", identity); err != nil {
		panic(err)
	}
	fmt.Println("target before:", headOf(target))
	if err := utils.SyncWorktreeFromRepo(ctx, target, store); err != nil {
		fmt.Println("sync err:", err)
	}
	fmt.Println("target after :", headOf(target))
	if content, err := os.ReadFile(filepath.Join(target, "script.json")); err != nil {
		fmt.Println("工作区 script.json:", err)
	} else {
		fmt.Println("工作区内容:", string(content))
	}
	fmt.Println("工作区是否还有 old.txt:", func() bool { _, e := os.Stat(filepath.Join(target, "old.txt")); return e == nil }())
}
