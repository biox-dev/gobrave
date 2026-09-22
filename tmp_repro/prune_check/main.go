package main

// 临时验证：install（SyncWorktreeFromRepo 的 reset 路径）在「本地有 store 不认识的提交」时
// 到底发生了什么，pruneLocalRefsUnknownToSource 会删掉哪些 ref。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/biox-dev/gobrave/internal/utils"
)

func refsOf(dir string) map[string]string {
	out := map[string]string{}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		out["<open err>"] = err.Error()
		return out
	}
	refs, err := repo.References()
	if err != nil {
		out["<refs err>"] = err.Error()
		return out
	}
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		out[ref.Name().String()] = ref.Hash().String()[:7] + ref.Target().String()
		return nil
	})
	return out
}

func show(title, dir string) {
	fmt.Printf("\n-- %s (%s)\n", title, dir)
	for name, v := range refsOf(dir) {
		fmt.Printf("   %-34s %s\n", name, v)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.txt")); err == nil {
		fmt.Println("   worktree: old.txt 仍在")
	} else {
		fmt.Println("   worktree: old.txt 已消失")
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	identity := utils.GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}

	root, err := os.MkdirTemp("", "prune_check_")
	if err != nil {
		panic(err)
	}
	fmt.Println("root:", root)

	// store：裸仓库，main 分支上是 "store 内容"
	src := filepath.Join(root, "src")
	os.MkdirAll(src, 0o755)
	repo, err := utils.EnsureGitRepo(src)
	if err != nil {
		panic(err)
	}
	os.WriteFile(filepath.Join(src, "script.json"), []byte(`{"v":"store"}`), 0o644)
	if _, err := utils.CommitAll(repo, "store v1", identity); err != nil {
		panic(err)
	}
	store := filepath.Join(root, "store")
	if _, err := utils.EnsureBareGitRepo(store); err != nil {
		panic(err)
	}
	if _, err := utils.PushDirToRepo(ctx, src, store); err != nil {
		panic(err)
	}
	storeHead, _ := git.PlainOpen(store)
	sh, _ := storeHead.Head()
	storeHash := sh.Hash()
	fmt.Println("store HEAD:", sh.Name().Short(), storeHash.String()[:7])

	// target：本地有「未发布提交」的脚本目录（模拟编辑后提交过但没 publish）
	target := filepath.Join(root, "target")
	os.MkdirAll(target, 0o755)
	trepo, err := utils.EnsureGitRepo(target)
	if err != nil {
		panic(err)
	}
	os.WriteFile(filepath.Join(target, "old.txt"), []byte("local only\n"), 0o644)
	if _, err := utils.CommitAll(trepo, "local only", identity); err != nil {
		panic(err)
	}
	th, _ := trepo.Head()
	localOnly := th.Hash()
	fmt.Println("target 本地独有提交:", localOnly.String()[:7])

	// 再保留一个「未使用的旧分支」，tip 也是 store 不认识的提交
	if err := trepo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("kept-branch"), localOnly)); err != nil {
		panic(err)
	}
	// 以及一个 tip 在 store 里的分支（模拟之前 install 过留下的分支）
	if err := trepo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName("dev"), storeHash)); err != nil {
		panic(err)
	}
	show("install 之前", target)

	fmt.Println("\n=== 调用 SyncWorktreeFromRepo(target <- store) ===")
	if err := utils.SyncWorktreeFromRepo(ctx, target, store); err != nil {
		fmt.Println("err:", err)
	} else {
		fmt.Println("ok")
	}
	show("install 之后", target)

	// 再看 target 里是否还找得到那个本地独有提交对象（reflog/悬空）
	trepo2, _ := git.PlainOpen(target)
	if _, err := trepo2.CommitObject(localOnly); err != nil {
		fmt.Println("\n本地独有提交对象:", err)
	} else {
		fmt.Println("\n本地独有提交对象仍在本地对象库（已悬空，无 ref 指向）")
	}
}
