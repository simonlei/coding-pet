package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/simonlei/coding-pet-dashboard/internal/notifier"
)

// runSubcommand 检查 args 是否属于 target 管理三件套；命中返回 (退出码, true)。
// 未命中返回 (0, false)，由 caller 走 daemon 分支。
//
// 支持的形态：
//
//	agent add target <url> [--kind wechat_work]
//	agent list targets
//	agent remove target <url-or-1-based-index>
//
// 常见词序错误（`agent target list`、`agent add`、`agent list`、`agent remove`）
// 会在这里被拦截并给出友好提示，避免走到 daemon 分支被 --server 报错误导。
func runSubcommand(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	// `agent target ...`：常见词序错，明示正确形态。
	if args[0] == "target" || args[0] == "targets" {
		fmt.Fprintln(os.Stderr, "usage: agent {add target <url> | list targets | remove target <url|index>}")
		return 2, true
	}
	verb := args[0]
	// 只有 add/list/remove 才可能是子命令；其他情况让 daemon 处理。
	switch verb {
	case "add", "list", "remove":
	default:
		return 0, false
	}
	if len(args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: agent %s %s\n", verb, subcmdUsageSuffix(verb))
		return 2, true
	}
	noun := args[1]
	switch {
	case verb == "add" && noun == "target":
		return runAddTarget(args[2:]), true
	case verb == "list" && noun == "targets":
		return runListTargets(args[2:]), true
	case verb == "remove" && noun == "target":
		return runRemoveTarget(args[2:]), true
	}
	fmt.Fprintf(os.Stderr, "unknown subcommand: agent %s %s\nusage: agent %s %s\n", verb, noun, verb, subcmdUsageSuffix(verb))
	return 2, true
}

// subcmdUsageSuffix 给出 verb 之后的正确参数形态。
func subcmdUsageSuffix(verb string) string {
	switch verb {
	case "add":
		return "target <url> [--kind wechat_work]"
	case "list":
		return "targets"
	case "remove":
		return "target <url-or-index>"
	}
	return ""
}

// isHelpFlag 判断一个 arg 是不是常见的 help flag。
func isHelpFlag(a string) bool {
	return a == "-h" || a == "--help" || a == "help"
}

// storeForCLI 构造一个 fileTargetStore 用于子命令。默认走
// $CODING_PET_TARGETS_PATH 或 ~/.coding-pet/targets.json；测试可通过环境变量覆盖。
func storeForCLI() *fileTargetStoreWrapper {
	return &fileTargetStoreWrapper{
		store: notifier.NewFileTargetStore(""),
		out:   os.Stdout,
		errw:  os.Stderr,
	}
}

// fileTargetStoreWrapper 只是为了让测试注入 out/errw；生产用 os.Stdout/Stderr。
type fileTargetStoreWrapper struct {
	store interface {
		Load() error
		Add(notifier.Target) error
		Remove(string) error
		List() []notifier.Target
		Path() string
	}
	out  io.Writer
	errw io.Writer
}

func runAddTarget(args []string) int {
	return runAddTargetWith(storeForCLI(), args)
}

func runAddTargetWith(w *fileTargetStoreWrapper, args []string) int {
	fs := flag.NewFlagSet("add target", flag.ContinueOnError)
	fs.SetOutput(w.errw)
	kind := fs.String("kind", "wechat_work", "target kind (default: wechat_work)")
	fs.Usage = func() {
		fmt.Fprintln(w.errw, "usage: agent add target <url> [--kind wechat_work]")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return 2
	}
	url := fs.Arg(0)
	// 先 Load 让 before 反映磁盘真实状态；err 只 log 不阻塞（Add 会再次 Load 并可能失败）。
	if err := w.store.Load(); err != nil {
		fmt.Fprintf(w.errw, "warn: load before add: %v\n", err)
	}
	before := len(w.store.List())
	if err := w.store.Add(notifier.Target{Kind: *kind, URL: url}); err != nil {
		fmt.Fprintf(w.errw, "add failed: %v\n", err)
		return 1
	}
	after := w.store.List()
	if len(after) == before {
		fmt.Fprintf(w.out, "target already present (kind=%s url=%s)\n", *kind, notifier.RedactURL(*kind, url))
	} else {
		fmt.Fprintf(w.out, "added: kind=%s url=%s\n", *kind, notifier.RedactURL(*kind, url))
	}
	printTargetList(w.out, after)
	fmt.Fprintf(w.out, "config: %s\n", w.store.Path())
	return 0
}

func runListTargets(args []string) int {
	return runListTargetsWith(storeForCLI(), args)
}

func runListTargetsWith(w *fileTargetStoreWrapper, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(w.errw, "usage: agent list targets")
		if isHelpFlag(args[0]) {
			return 0
		}
		return 2
	}
	if err := w.store.Load(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(w.errw, "load: %v\n", err)
			// Load 报错但可能只是文件损坏；仍打印现有内存缓存（Load 保留旧值）。
		}
	}
	printTargetList(w.out, w.store.List())
	fmt.Fprintf(w.out, "config: %s\n", w.store.Path())
	return 0
}

func runRemoveTarget(args []string) int {
	return runRemoveTargetWith(storeForCLI(), args)
}

func runRemoveTargetWith(w *fileTargetStoreWrapper, args []string) int {
	if len(args) < 1 || isHelpFlag(args[0]) {
		fmt.Fprintln(w.errw, "usage: agent remove target <url-or-index>")
		if len(args) < 1 {
			return 2
		}
		return 0
	}
	key := args[0]
	if err := w.store.Remove(key); err != nil {
		fmt.Fprintf(w.errw, "remove failed: %v\n", err)
		return 1
	}
	fmt.Fprintf(w.out, "removed: %s\n", key)
	printTargetList(w.out, w.store.List())
	fmt.Fprintf(w.out, "config: %s\n", w.store.Path())
	return 0
}

// printTargetList 输出用户可读的 target 列表（序号 + kind + 脱敏 URL）。
func printTargetList(out io.Writer, targets []notifier.Target) {
	if len(targets) == 0 {
		fmt.Fprintln(out, "(no targets)")
		return
	}
	for i, t := range targets {
		fmt.Fprintf(out, "%s) %s\t%s\n", strconv.Itoa(i+1), t.Kind, notifier.RedactURL(t.Kind, t.URL))
	}
}
