package main

// 临时验证：InstallScript/InstallWorkflow 走的 SyncWorktreeFromRepo，其目标目录分支
// 是否与 store 裸仓库的分支一致（首次安装 / 重复安装 / 目标已有别的分支）。

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

// newStore 造一个 store 裸仓库：分支名由 branch 指定，HEAD 指向该分支（模拟 DownloadStore
// 从「默认分支是 master 的上游」克隆出来的结果）。
func newStore(root, branch string, identity utils.GitIdentity) string {
	ctx := context.Background()
	src := filepath.Join(root, "upstream_src_"+branch)
	os.MkdirAll(src, 0o755)
	repo, err := git.PlainInitWithOptions(src, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName(branch)},
	})
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(src, "script.json"), []byte(`{"script_id":"s1"}`), 0o644); err != nil {
		panic(err)
	}
	if _, err := utils.CommitAll(repo, "init "+branch, identity); err != nil {
		panic(err)
	}

	store := filepath.Join(root, "store_"+branch)
	if _, err := utils.EnsureBareGitRepo(store); err != nil {
		panic(err)
	}
	if _, err := utils.PushDirToRepo(ctx, src, store); err != nil {
		panic(err)
	}
	sRepo, err := git.PlainOpen(store)
	if err != nil {
		panic(err)
	}
	if err := sRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch))); err != nil {
		panic(err)
	}
	return store
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	identity := utils.GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}

	root, err := os.MkdirTemp("", "branch_check4_")
	if err != nil {
		panic(err)
	}
	fmt.Println("root:", root)

	for _, branch := range []string{"main", "master"} {
		fmt.Printf("\n########## store 分支 = %s ##########\n", branch)
		store := newStore(root, branch, identity)
		fmt.Println("store HEAD :", headOf(store))

		// 1) 首次安装：目标目录不存在
		target1 := filepath.Join(root, "install_"+branch, "fresh", "s1")
		if err := utils.SyncWorktreeFromRepo(ctx, target1, store); err != nil {
			fmt.Println("(1) 首次安装 err:", err)
		} else {
			fmt.Println("(1) 首次安装 目标 HEAD:", headOf(target1))
		}

		// 2) 重复安装：同一个目标目录再装一次
		if err := utils.SyncWorktreeFromRepo(ctx, target1, store); err != nil {
			fmt.Println("(2) 重复安装 err:", err)
		} else {
			fmt.Println("(2) 重复安装 目标 HEAD:", headOf(target1))
		}

		// 3) 目标目录已存在、但分支是另一个（模拟本地已有脚本目录）
		other := "master"
		if branch == "master" {
			other = "main"
		}
		target3 := filepath.Join(root, "install_"+branch, "existing", "s1")
		os.MkdirAll(target3, 0o755)
		repo3, err := git.PlainInitWithOptions(target3, &git.PlainInitOptions{
			InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName(other)},
		})
		if err != nil {
			panic(err)
		}
		os.WriteFile(filepath.Join(target3, "old.txt"), []byte("old\n"), 0o644)
		if _, err := utils.CommitAll(repo3, "old", identity); err != nil {
			panic(err)
		}
		fmt.Printf("(3) 目标已存在(分支 %s) HEAD: %s\n", other, headOf(target3))
		if err := utils.SyncWorktreeFromRepo(ctx, target3, store); err != nil {
			fmt.Println("(3) 安装 err:", err)
		} else {
			fmt.Println("(3) 安装后 目标 HEAD:", headOf(target3))
		}
	}
}
