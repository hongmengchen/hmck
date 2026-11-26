package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"
)

// syncOptions 是一条同步命令的选项。密码只从 stdin（脚本）或终端读，
// 不接受命令行参数：argv 会留在 shell 历史和 ps 里。
type syncOptions struct {
	repo      string
	force     bool
	dryRun    bool
	passStdin bool
	quiet     bool
	// pass 是 TUI 在页面里输入的同步密码：只在内存里待这一次调用，
	// 不写盘、不进命令行参数。
	pass string
}

// passphrase 决定这次动作要不要向人要素材：TUI 已经给了就直接用，否则走终端/stdin。
func (o syncOptions) passphrase(confirm bool) (string, error) {
	if o.pass != "" {
		return o.pass, nil
	}
	return readSyncPassphrase(o.passStdin, confirm)
}

func runSync(args []string) int {
	if len(args) == 0 {
		code := runSyncStatus(nil)
		st, _ := loadSyncState()
		if !syncEnabled(st) {
			fmt.Println()
			fmt.Println("开始：hmck sync push（自动建私有仓库、设同步密码）")
			fmt.Println("另一台机器：hmck sync pull（输入同一个密码）")
		}
		return code
	}
	switch args[0] {
	case "status":
		return runSyncStatus(args[1:])
	case "push":
		return runSyncPush(args[1:])
	case "pull":
		return runSyncPull(args[1:])
	case "add":
		return runSyncAdd(args[1:])
	case "rm", "remove":
		return runSyncRemove(args[1:])
	case "off", "reset":
		return runSyncOff(args[1:])
	case "help", "-h", "--help":
		syncUsage(os.Stdout)
		return 0
	}
	// hmck sync --json 就是 hmck sync status --json
	if strings.HasPrefix(args[0], "-") {
		return runSyncStatus(args)
	}
	fmt.Fprintf(os.Stderr, "未知的 sync 子命令: %s\n\n", args[0])
	syncUsage(os.Stderr)
	return 2
}

func syncUsage(w io.Writer) {
	fmt.Fprintf(w, `hmck sync — 多端同步本机工具配置（GitHub 私有仓库，加密存放）

用法:
  hmck sync                 看同步状态（仓库、密钥、上次推送/拉取、设备、同步清单）
  hmck sync --json          同样的状态，给脚本/agent 解析
  hmck sync push            把本机配置加密后推到仓库；第一次会建仓库、设密码
  hmck sync pull            把仓库里的配置拉下来（--dry-run 逐文件预览文本差异）
  hmck sync add <路径>      额外同步一个文件（config:… / home:… / ~/…）
  hmck sync rm <路径>       去掉一个额外路径
  hmck sync off             忘掉本机的仓库和同步密码（远端数据不动）

push 选项:
  --force              远端有别的设备推的新版本时也覆盖（默认先让你 pull）
  --passphrase-stdin   从 stdin 读同步密码，给脚本/agent 用
  --repo owner/name    换一个仓库（默认 <你的 GitHub 用户名>/%s）

pull 选项:
  --dry-run            只解密预览，不写本机文件
  --force              本机有没推上去的改动也覆盖
  --passphrase-stdin  同上
  --repo owner/name    同上

登录: 借用 gh CLI。没登录就先运行 gh auth login。
加密: PBKDF2-HMAC-SHA256（%d 次）派生密钥 + AES-256-GCM；GitHub 上只有密文。
`, syncRepoName, syncKDFIter)
}

func runSyncStatus(args []string) int {
	fs := flag.NewFlagSet("sync status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "参数不对：hmck sync status [--json]")
		return 2
	}

	st, err := loadSyncState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 1
	}
	names, nerr := syncNames(st)
	if nerr != nil {
		fmt.Fprintf(os.Stderr, "同步清单有问题: %v\n", nerr)
	}
	gh := "未安装"
	if _, ok := ghPath(); ok {
		gh = "已安装（登录状态在 push/pull 时检查）"
	}

	if *asJSON {
		data, _ := json.MarshalIndent(syncStatusJSON(st, names, gh), "", "  ")
		fmt.Println(string(data))
		return 0
	}

	state := "未开启（运行 hmck sync push 开始）"
	if syncEnabled(st) {
		state = "已开启"
	}
	fmt.Printf("同步     %s\n", state)
	repo := st.Repo
	if repo == "" {
		repo = "(还没认仓库；第一次 push 自动建成 <用户名>/" + syncRepoName + ")"
	}
	fmt.Printf("仓库     %s\n", repo)
	keyLine := "未设置（push 时输入密码）"
	if st.Key != "" {
		keyLine = "已记住密钥（本机 sync.json，0600）"
	}
	fmt.Printf("加密     %s\n", keyLine)
	fmt.Printf("gh       %s\n", gh)
	if st.LastPush != "" {
		fmt.Printf("上次推送 %s\n", st.LastPush)
	}
	if st.LastPull != "" {
		fmt.Printf("上次拉取 %s\n", st.LastPull)
	}
	if st.LastError != "" {
		fmt.Printf("上次失败 %s\n", st.LastError)
	}
	if len(st.Devices) == 0 {
		fmt.Printf("设备     还没有记录（push/pull 之后会记下来）\n")
	} else {
		fmt.Printf("设备     %d 台\n", len(st.Devices))
		for _, d := range st.Devices {
			tag := ""
			if d.ID != "" && d.ID == st.DeviceID {
				tag = "（这台）"
			}
			fmt.Printf("  · %s%s  %s\n", deviceDisplayName(d), tag, deviceBadge(d))
		}
	}
	fmt.Printf("文件     %d 个\n", len(names))
	for _, n := range names {
		abs, err := syncNamePath(n)
		mark := "  "
		if err != nil {
			mark = "! "
			abs = n
		} else if info, err := os.Stat(abs); err == nil {
			mark = "✓ "
			abs += "  " + humanSize(info.Size())
		} else {
			mark = "· "
			abs += "  （这台机器没有）"
		}
		fmt.Printf("  %s%s\n", mark, shortenHome(abs))
	}
	return 0
}

// syncStatusJSON 是 hmck sync status --json 的内容（agent 也读这份）
func syncStatusJSON(st syncState, names []string, gh string) map[string]any {
	devices := st.Devices
	if devices == nil {
		devices = []syncDevice{} // 空表也输出 []，脚本不用处理 null
	}
	return map[string]any{
		"enabled":     syncEnabled(st),
		"repo":        st.Repo,
		"gh":          gh,
		"has_key":     st.Key != "",
		"files":       names,
		"extra_paths": st.ExtraPaths,
		"last_push":   st.LastPush,
		"last_pull":   st.LastPull,
		"last_error":  st.LastError,
		"device_id":   st.DeviceID,
		"devices":     devices,
	}
}

func runSyncPush(args []string) int {
	opts, code := parseSyncActionFlags("push", args)
	if code != 0 {
		return code
	}
	msg, err := syncPush(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	if !opts.quiet {
		fmt.Println("✓ " + msg)
	}
	return 0
}

func runSyncPull(args []string) int {
	opts, code := parseSyncActionFlags("pull", args)
	if code != 0 {
		return code
	}
	var preview []syncPreviewFile
	msg, err := syncPullWithPreview(opts, &preview)
	if err != nil {
		fmt.Fprintf(os.Stderr, "✗ %v\n", err)
		return 1
	}
	if !opts.quiet {
		fmt.Println("✓ " + msg)
		if opts.dryRun {
			fmt.Print(renderSyncPreview(preview))
		}
	}
	return 0
}

func parseSyncActionFlags(name string, args []string) (syncOptions, int) {
	fs := flag.NewFlagSet("sync "+name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var opts syncOptions
	fs.StringVar(&opts.repo, "repo", "", "仓库 owner/name")
	fs.BoolVar(&opts.force, "force", false, "覆盖远端/本机的冲突版本")
	fs.BoolVar(&opts.dryRun, "dry-run", false, "只看会改什么，不写文件")
	fs.BoolVar(&opts.passStdin, "passphrase-stdin", false, "从 stdin 读同步密码")
	if err := fs.Parse(reorderFlags(args)); err != nil {
		fmt.Fprintf(os.Stderr, "参数不对：hmck sync %s [--repo owner/name] [--force] [--dry-run] [--passphrase-stdin]\n", name)
		return opts, 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "hmck sync %s 不带位置参数\n", name)
		return opts, 2
	}
	if opts.repo != "" {
		if _, _, err := splitSyncRepo(opts.repo); err != nil {
			fmt.Fprintln(os.Stderr, err.Error())
			return opts, 2
		}
	}
	return opts, 0
}

// readSyncPassphrase 从终端读密码（不回显），脚本里用 --passphrase-stdin。
// 第一次 push 要输入两遍，确认没打错——密码丢了仓库里的数据就解不开了。
func readSyncPassphrase(fromStdin, confirm bool) (string, error) {
	if fromStdin {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("读 stdin 失败: %w", err)
		}
		pass := strings.TrimRight(line, "\r\n")
		if strings.TrimSpace(pass) == "" {
			return "", errors.New("从 stdin 读到的同步密码是空的")
		}
		return pass, nil
	}
	if !isTTY(os.Stdin) {
		return "", errors.New("没有终端可输入同步密码；脚本里加 --passphrase-stdin 从 stdin 读")
	}
	fmt.Fprint(os.Stderr, "同步密码（加密用，不上传、不落盘明文）：")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("读密码失败: %w", err)
	}
	pass := string(first)
	if strings.TrimSpace(pass) == "" {
		return "", errors.New("同步密码不能为空")
	}
	if confirm {
		fmt.Fprint(os.Stderr, "再输入一次：")
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", fmt.Errorf("读密码失败: %w", err)
		}
		if string(second) != pass {
			return "", errors.New("两次输入的密码不一样")
		}
	}
	return pass, nil
}

// syncRemote 是 push 的连接结果：认人、定仓库。
func syncRemote(st syncState) (*ghClient, string, string, error) {
	client, err := newGHClient()
	if err != nil {
		return nil, "", "", err
	}
	login, err := client.login()
	if err != nil {
		return nil, "", "", err
	}
	repo := st.Repo
	if repo == "" {
		repo = login + "/" + syncRepoName
	}
	return client, login, repo, nil
}

// syncKeyForPush 决定这次加密用哪把钥匙：
//   - 本机记住钥匙：先用它解一次远端（如果远端有数据），验证没串台；
//   - 本机没钥匙：让用户输密码，远端有数据就用远端的 salt 派生并验证，
//     远端没有就新生成 salt。
//
// 派生用的 salt/iter 会存进 sync.json，并且每次推送原样写进信封——换机器的人
// 靠它们从同一个密码算出同一把钥匙。
func syncKeyForPush(st *syncState, remote []byte, remoteExists bool, opts syncOptions) ([]byte, error) {
	if st.Key != "" {
		key, err := syncKeyBytes(*st)
		if err != nil {
			return nil, err
		}
		if remoteExists {
			if _, err := decryptSyncPayload(key, remote); err != nil {
				// --force 是用户明确说“拿本机覆盖远端”，这时不跟着远端的信封换密钥，
				// 不然覆盖一次又被远端解不开的数据卡住。
				if !opts.force {
					return nil, errors.New("本机记住的密钥解不开远端数据（另一台设备换过密码？先 hmck sync off，再 hmck sync pull）")
				}
			} else if env, err := parseSyncEnvelope(remote); err == nil {
				st.Salt, st.Iter = env.Salt, env.Iter
			}
		}
		if len(st.Salt) < 8 || st.Iter == 0 {
			salt, err := randomBytes(syncSaltLen)
			if err != nil {
				return nil, err
			}
			st.Salt, st.Iter = salt, syncKDFIter
		}
		return key, nil
	}

	pass, err := opts.passphrase(!remoteExists)
	if err != nil {
		return nil, err
	}
	if remoteExists {
		key, _, err := decryptWithPassphrase(pass, remote)
		if err != nil {
			return nil, errors.New("同步密码不对：解不开远端已有的数据")
		}
		env, err := parseSyncEnvelope(remote)
		if err != nil {
			return nil, err
		}
		st.Salt, st.Iter = env.Salt, env.Iter
		st.Key = base64.StdEncoding.EncodeToString(key)
		return key, nil
	}

	salt := st.Salt
	if len(salt) < 8 {
		salt, err = randomBytes(syncSaltLen)
		if err != nil {
			return nil, err
		}
	}
	iter := st.Iter
	if iter == 0 {
		iter = syncKDFIter
	}
	key, err := deriveSyncKey(pass, salt, iter)
	if err != nil {
		return nil, err
	}
	st.Salt, st.Iter = salt, iter
	st.Key = base64.StdEncoding.EncodeToString(key)
	return key, nil
}

func syncPush(opts syncOptions) (string, error) {
	syncMu.Lock()
	defer syncMu.Unlock()

	st, err := loadSyncState()
	if err != nil {
		return "", err
	}
	client, login, repo, err := syncRemote(st)
	if err != nil {
		return "", err
	}
	if opts.repo != "" && opts.repo != st.Repo {
		// 换仓库等于重新开始，旧的远端版本号不能拿来对比
		st.Repo, st.LastSHA, st.LastHash = opts.repo, "", ""
	}
	st.Repo = repo

	owner, name, err := splitSyncRepo(repo)
	if err != nil {
		return "", err
	}
	exists, private, err := client.repoInfo(repo)
	if err != nil {
		return "", err
	}
	if !exists {
		if owner != login {
			return "", fmt.Errorf("仓库 %s 不存在；只会在你自己的账号（%s）名下自动创建，别的账号请先在 GitHub 上建好", repo, login)
		}
		if err := client.createRepo(name); err != nil {
			return "", err
		}
		private = true
	}
	if !private {
		return "", fmt.Errorf("仓库 %s 不是私有的；同步数据只放私有仓库，请先把它改成私有", repo)
	}

	remote, sha, err := client.getFile(repo, syncRemoteFile)
	if err != nil {
		return "", err
	}
	if sha != "" && !opts.force {
		switch {
		case st.LastSHA == "":
			return "", fmt.Errorf("远端已经有同步数据（%s）。先 hmck sync pull 拉下来；确认要拿本机覆盖就用 --force", repo)
		case st.LastSHA != sha:
			return "", errors.New("别的设备推过新版本。先 hmck sync pull；确认要覆盖就用 --force")
		}
	}

	key, err := syncKeyForPush(&st, remote, sha != "", opts)
	if err != nil {
		return "", err
	}

	files, err := collectSyncFiles(st)
	if err != nil {
		return "", err
	}
	paths := st.ExtraPaths
	var devices []syncDevice
	// 推之前先把远端的文件和设备表合进来：A 机器有的配置，B 机器没有时推一次不能把它抹掉。
	// 同名文件以本机为准；设备表按 ID 合并，各机器的推送时间都留着。
	if sha != "" {
		if remotePlain, derr := decryptSyncPayload(key, remote); derr == nil {
			var rp syncPayload
			if json.Unmarshal(remotePlain, &rp) == nil && rp.Version == syncFormat {
				files = mergeSyncFiles(files, rp.Files)
				paths = mergeSyncPaths(paths, rp.Paths)
				devices = rp.Devices
			}
		}
	}
	hash, err := payloadHash(files, paths)
	if err != nil {
		return "", err
	}
	if sha != "" && sha == st.LastSHA && hash == st.LastHash && !opts.force {
		return "本地配置没变，不用推", nil
	}
	st.ExtraPaths = paths

	// 这台机器也在设备表里记一笔，和别的机器合并后一起加密推上去。
	// 先合并再记时间：远端那台同名设备的时间可能比本机缓存的新。
	devices = mergeSyncDevices(st.Devices, devices)
	st.Devices = devices
	devices = upsertSyncDevice(devices, touchLocalDevice(&st, true, false))
	st.Devices = devices

	plain, err := json.Marshal(syncPayload{
		Version:   syncFormat,
		UpdatedAt: time.Now().UTC(),
		Paths:     paths,
		Files:     files,
		Devices:   devices,
	})
	if err != nil {
		return "", err
	}
	envelope, err := encryptSyncPayload(key, plain, st.Salt, st.Iter)
	if err != nil {
		return "", err
	}
	newSHA, err := client.putFile(repo, syncRemoteFile, envelope, sha, "hmck: 同步本机配置")
	if err != nil {
		return "", err
	}

	st.LastSHA, st.LastHash = newSHA, hash
	st.LastPush = time.Now().Format("2006-01-02 15:04:05")
	st.LastError = ""
	if _, err := saveSyncState(st); err != nil {
		return "", fmt.Errorf("已推到 GitHub，但本机状态没保存: %w", err)
	}
	return fmt.Sprintf("已推送 %d 个文件到 %s（加密后 %s）", len(files), repo, humanSize(int64(len(envelope)))), nil
}

func syncPull(opts syncOptions) (string, error) {
	return syncPullWithPreview(opts, nil)
}

// syncPullWithPreview shares the normal pull path with CLI/TUI dry-runs. Preview
// data is returned only when the caller asks for it and opts.dryRun is true.
func syncPullWithPreview(opts syncOptions, previewOut *[]syncPreviewFile) (string, error) {
	syncMu.Lock()
	defer syncMu.Unlock()

	st, err := loadSyncState()
	if err != nil {
		return "", err
	}
	client, err := newGHClient()
	if err != nil {
		return "", err
	}
	login, err := client.login()
	if err != nil {
		return "", err
	}

	repo := st.Repo
	if opts.repo != "" {
		repo = opts.repo
	}
	discovered := false
	if repo == "" {
		candidate := login + "/" + syncRepoName
		exists, _, err := client.repoInfo(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return "", fmt.Errorf("这台机器还没连过同步，也没找到 %s；先在另一台机器 hmck sync push，或用 --repo owner/name 指定", candidate)
		}
		repo, discovered = candidate, true
	}
	exists, private, err := client.repoInfo(repo)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("仓库 %s 不存在", repo)
	}
	if !private {
		return "", fmt.Errorf("仓库 %s 不是私有的；不同步非私有仓库", repo)
	}

	remote, sha, err := client.getFile(repo, syncRemoteFile)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "", fmt.Errorf("仓库 %s 里还没有同步数据；先在别的设备 hmck sync push", repo)
	}

	var key, plain []byte
	if st.Key != "" {
		key, err = syncKeyBytes(st)
		if err != nil {
			return "", err
		}
		plain, err = decryptSyncPayload(key, remote)
		if err != nil {
			return "", errors.New("本机记住的密钥解不开远端数据（另一台设备换过密码？先 hmck sync off 再 pull）")
		}
	} else {
		pass, err := opts.passphrase(false)
		if err != nil {
			return "", err
		}
		key, plain, err = decryptWithPassphrase(pass, remote)
		if err != nil {
			return "", errors.New("同步密码不对：解不开远端数据")
		}
		st.Key = base64.StdEncoding.EncodeToString(key)
	}
	env, err := parseSyncEnvelope(remote)
	if err != nil {
		return "", err
	}
	st.Salt, st.Iter = env.Salt, env.Iter

	var p syncPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		return "", fmt.Errorf("同步内容不是合法 JSON: %w", err)
	}
	if p.Version != syncFormat {
		return "", fmt.Errorf("同步内容版本 %d 不认识（本机 hmck 太旧？）", p.Version)
	}

	devicePayload, err := filterSyncPayloadForDevice(p, st)
	if err != nil {
		return "", err
	}
	var conflicts []string
	if !opts.force && st.LastHash != "" {
		conflicts = diffAgainstPayload(devicePayload)
		if !opts.dryRun && len(conflicts) > 0 {
			preview := conflicts
			if len(preview) > 3 {
				preview = append(append([]string(nil), preview[:3]...), "…")
			}
			return "", fmt.Errorf("本机有 %d 个文件和远端不一样（%s），pull 会覆盖本机改动。先 hmck sync push，或加 --force", len(conflicts), strings.Join(preview, "、"))
		}
	}

	written, same, previewFiles, err := applySyncPayload(devicePayload, opts.dryRun, opts.dryRun && previewOut != nil)
	if err != nil {
		return "", err
	}
	if previewOut != nil {
		*previewOut = previewFiles
	}
	if opts.dryRun {
		msg := fmt.Sprintf("预览：会写入 %d 个文件，%d 个没变（%s）", len(written), len(same), repo)
		if len(conflicts) > 0 {
			msg += fmt.Sprintf("；有 %d 个本机冲突，普通 pull 会拒绝覆盖", len(conflicts))
		}
		return msg, nil
	}

	hash, err := payloadHash(p.Files, mergeSyncPaths(st.ExtraPaths, p.Paths))
	if err == nil {
		st.LastHash = hash
	}
	st.Repo = repo
	st.LastSHA = sha
	st.LastPull = time.Now().Format("2006-01-02 15:04:05")
	st.LastError = ""
	st.ExtraPaths = mergeSyncPaths(st.ExtraPaths, p.Paths)
	st.Devices = mergeSyncDevices(st.Devices, p.Devices)
	st.Devices = upsertSyncDevice(st.Devices, touchLocalDevice(&st, false, true))
	if _, err := saveSyncState(st); err != nil {
		return "", fmt.Errorf("文件已写入，但本机状态没保存: %w", err)
	}
	if discovered {
		return fmt.Sprintf("已连接 %s：写入 %d 个文件，%d 个没变", repo, len(written), len(same)), nil
	}
	return fmt.Sprintf("已从 %s 拉取：写入 %d 个文件，%d 个没变", repo, len(written), len(same)), nil
}

// refreshSyncDevices 只读远端的设备表，更新本机缓存：不动任何配置文件，
// 也不更新 LastSHA（否则下次 push 会以为远端没变，跳过「别的设备推过」的提醒）。
func refreshSyncDevices() (string, error) {
	syncMu.Lock()
	defer syncMu.Unlock()

	st, err := loadSyncState()
	if err != nil {
		return "", err
	}
	if !syncEnabled(st) {
		return "", errors.New("这台机器还没连同步")
	}
	key, err := syncKeyBytes(st)
	if err != nil {
		return "", err
	}
	client, err := newGHClient()
	if err != nil {
		return "", err
	}
	remote, sha, err := client.getFile(st.Repo, syncRemoteFile)
	if err != nil {
		return "", err
	}
	if sha == "" {
		return "", fmt.Errorf("仓库 %s 里还没有同步数据", st.Repo)
	}
	plain, err := decryptSyncPayload(key, remote)
	if err != nil {
		return "", errors.New("本机记住的密钥解不开远端数据")
	}
	var p syncPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		return "", fmt.Errorf("同步内容不是合法 JSON: %w", err)
	}
	st.Devices = mergeSyncDevices(st.Devices, p.Devices)
	if _, err := saveSyncState(st); err != nil {
		return "", err
	}
	return fmt.Sprintf("设备列表已更新：%d 台", len(st.Devices)), nil
}

// mergeSyncPaths 把远端带回来的额外路径并进本机清单：只增不减，
// 本机自己加的不会被远端删掉。
func mergeSyncPaths(local, remote []string) []string {
	seen := make(map[string]bool, len(local)+len(remote))
	out := make([]string, 0, len(local)+len(remote))
	for _, list := range [][]string{local, remote} {
		for _, p := range list {
			p = strings.TrimSpace(p)
			if p == "" || seen[p] {
				continue
			}
			if _, err := syncNamePath(p); err != nil {
				continue
			}
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// addSyncPath 把用户给的路径收进同步清单，返回存下来的逻辑名。
// added=false 表示本来就在清单里；TUI 和 CLI 共用这一段。
func addSyncPath(raw string) (name string, added bool, err error) {
	st, err := loadSyncState()
	if err != nil {
		return "", false, err
	}
	name, err = normalizeSyncPath(raw)
	if err != nil {
		return "", false, err
	}
	if name == syncStateName {
		return "", false, errors.New("sync.json 是本机的同步状态和密钥，不能同步")
	}
	for _, p := range st.ExtraPaths {
		if p == name {
			return name, false, nil
		}
	}
	st.ExtraPaths = append(st.ExtraPaths, name)
	sort.Strings(st.ExtraPaths)
	if _, err := saveSyncState(st); err != nil {
		return "", false, err
	}
	return name, true, nil
}

func runSyncAdd(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "用法: hmck sync add <路径>")
		return 2
	}
	name, added, err := addSyncPath(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		if _, nerr := normalizeSyncPath(args[0]); nerr != nil {
			return 2
		}
		return 1
	}
	if !added {
		fmt.Printf("%s 已经在同步清单里了\n", name)
		return 0
	}
	fmt.Printf("已加入同步清单: %s\n", name)
	fmt.Println("运行 hmck sync push 推到远端")
	return 0
}

func runSyncRemove(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "用法: hmck sync rm <路径>")
		return 2
	}
	st, err := loadSyncState()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 1
	}
	name, err := normalizeSyncPath(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误: "+err.Error())
		return 2
	}
	var out []string
	found := false
	for _, p := range st.ExtraPaths {
		if p == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		fmt.Fprintf(os.Stderr, "同步清单里没有 %s\n", name)
		return 1
	}
	st.ExtraPaths = out
	if _, err := saveSyncState(st); err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 1
	}
	fmt.Printf("已从同步清单去掉: %s\n", name)
	fmt.Println("运行 hmck sync push 让远端也更新")
	return 0
}

// disableSync 让本机忘掉仓库和同步密钥；远端仓库和里面的数据不动。
// 返回原来的仓库名，方便告诉用户远端还在哪。
func disableSync() (string, error) {
	st, err := loadSyncState()
	if err != nil {
		return "", err
	}
	repo := st.Repo
	st.Repo, st.Key, st.Salt, st.Iter = "", "", nil, 0
	st.LastSHA, st.LastHash, st.LastPush, st.LastPull, st.LastError = "", "", "", "", ""
	if _, err := saveSyncState(st); err != nil {
		return "", err
	}
	return repo, nil
}

func runSyncOff(args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "用法: hmck sync off")
		return 2
	}
	repo, err := disableSync()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: %v\n", err)
		return 1
	}
	fmt.Println("已忘掉本机的仓库和同步密钥（远端数据没动）")
	if repo != "" {
		fmt.Printf("远端仓库 %s 里的内容还在；要删去 GitHub 上删仓库\n", repo)
	}
	return 0
}

// autoSyncPush 是「保存后自动推」的入口：没配同步就安静跳过。
// 冲突/网络错误只返回给调用方显示，不打断工具本身。
func autoSyncPush() (string, error) {
	st, err := loadSyncState()
	if err != nil || !syncEnabled(st) {
		return "", nil
	}
	return syncPush(syncOptions{quiet: true})
}

// autoSyncAfterToolRun 是命令行版：推成功了说一句，失败给提示但不改退出码。
func autoSyncAfterToolRun() {
	msg, err := autoSyncPush()
	if err != nil {
		fmt.Fprintf(os.Stderr, "同步（自动推送）没成：%v\n", err)
		return
	}
	if msg != "" {
		fmt.Println("同步    " + msg)
	}
}
