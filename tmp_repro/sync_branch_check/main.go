package main

// 临时验证：SyncWorktreeFromRepo 到底同步了 store 的哪些分支？
// store 上准备两个分支（main / dev），分别看 clone 路径与 reset 路径的结果。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/biox-dev/gobrave/internal/utils"
)

func refsOf(dir string) []string {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return []string{"<open err: " + err.Error() + ">"}
	}
	refs, err := repo.References()
	if err != nil {
		return []string{"<refs err: " + err.Error() + ">"}
	}
	var out []string
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name() == plumbing.HEAD {
			out = append(out, fmt.Sprintf("HEAD -> %s", ref.Target()))
			return nil
		}
		out = append(out, fmt.Sprintf("%-28s %s", ref.Name(), ref.Hash().String()[:7]))
		return nil
	})
	sort.Strings(out)
	return out
}

func show(title, dir string) {
	fmt.Printf("\n-- %s\n", title)
	for _, l := range refsOf(dir) {
		fmt.Println("   ", l)
	}
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	identity := utils.GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}

	root, err := os.MkdirTemp("", "sync_check_")
	if err != nil {
		panic(err)
	}
	fmt.Println("root:", root)

	// store：先由 main 源发布，再由 dev 源发布 -> store 上有 dev（当前分支）与 main 两个分支
	mainSrc := filepath.Join(root, "main-src")
	if _, err := utils.EnsureGitRepo(mainSrc); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(mainSrc, "main.txt"), []byte("main\n"), 0o644); err != nil {
		panic(err)
	}
	mainRepo, _ := git.PlainOpen(mainSrc)
	if _, err := utils.CommitAll(mainRepo, "main", identity); err != nil {
		panic(err)
	}

	devSrc := filepath.Join(root, "dev-src")
	os.MkdirAll(devSrc, 0o755)
	devRepo, err := git.PlainInitWithOptions(devSrc, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("dev")},
	})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(devSrc, "dev.txt"), []byte("dev\n"), 0o644); err != nil {
		panic(err)
	}
	if _, err := utils.CommitAll(devRepo, "dev", identity); err != nil {
		panic(err)
	}

	store := filepath.Join(root, "store")
	if _, err := utils.EnsureBareGitRepo(store); err != nil {
		panic(err)
	}
	if _, err := utils.PushDirToRepo(ctx, mainSrc, store); err != nil {
		panic(err)
	}
	if _, err := utils.PushDirToRepo(ctx, devSrc, store); err != nil {
		panic(err)
	}
	show("store（裸仓库，HEAD 在 dev 上，另有 main 分支）", store)

	// 场景 1：目标目录不存在 -> clone 路径
	target1 := filepath.Join(root, "install-clone")
	if err := utils.SyncWorktreeFromRepo(ctx, target1, store); err != nil {
		fmt.Println("clone 路径 err:", err)
	}
	show("场景1 clone 之后（目标目录）", target1)
	entries, _ := os.ReadDir(target1)
	names := []string{}
	for _, e := range entries {
		if e.Name() != ".git" {
			names = append(names, e.Name())
		}
	}
	fmt.Println("   工作区文件:", names)

	// 场景 2：目标目录已存在（先按 main 分支建好）-> reset 路径
	target2 := filepath.Join(root, "install-reset")
	os.MkdirAll(target2, 0o755)
	t2repo, err := utils.EnsureGitRepo(target2)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(target2, "local.txt"), []byte("local\n"), 0o644); err != nil {
		panic(err)
	}
	if _, err := utils.CommitAll(t2repo, "local", identity); err != nil {
		panic(err)
	}
	show("场景2 同步之前（目标目录本地 main）", target2)
	if err := utils.SyncWorktreeFromRepo(ctx, target2, store); err != nil {
		fmt.Println("reset 路径 err:", err)
	}
	show("场景2 reset 之后（目标目录）", target2)
	entries2, _ := os.ReadDir(target2)
	var names2 []string
	for _, e := range entries2 {
		if e.Name() != ".git" {
			names2 = append(names2, e.Name())
		}
	}
	fmt.Println("   工作区文件:", names2)
}
