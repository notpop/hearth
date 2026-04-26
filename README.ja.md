# Hearth

> 家庭内ネットワーク向け分散タスクランナー。シングルバイナリ、デフォルト mTLS、ワーカー登録は1ファイル。

[English README](./README.md)

Hearth は、家にある複数台の PC(Mac, Windows, Linux, NAS など)に CPU 重め処理を分散させるためのツールです。例えば「画像→PDF変換」「動画エンコード」「バッチ ML 推論」のような重い処理を、メインPCからキューに積み、別マシンの worker が拾って処理し、結果が返ってくる ── これを **家庭内 LAN に閉じたまま** 実現します。親と子の役割は OS を問わず任意に選べます。

## 状態

**Alpha**。 wire プロトコル、公開 API、bundle のフォーマットは安定前です。利用する場合は commit を pin してください。

## なぜ Hearth?

家には3〜5台の PC があるのに、重い処理を投げると1台が詰まる ── という状況をよく見ます。クラウドキュー(SQS、Celery + Redis、Temporal)で同じ問題は解決できますが、**LAN を出るか、運用すべきインフラが増える**ため、家庭用には過剰です。Hearth は最小構成として:

- **LAN内で完結**(mDNS で自動発見、外部依存ゼロ)
- **シングルバイナリ**(Go、cgoなし、全 OS にクロスコンパイル可能)
- **mTLS**(自前 CA で全通信を暗号化+認証)
- **1ファイルでワーカー追加**(USB / SD / scp で渡すだけ)
- **ワーカーの離脱に追従**(リース + 自動再キュー)

を提供します。

## 用語

| 用語 | 意味 |
|---|---|
| **Coordinator** | キュー所有・リース管理・blob 保管・gRPC API を提供する**1台**の中心ホスト。常時稼働しているマシンに置く。 |
| **Worker** | 1つ以上の *kind* のジョブを取りに行き、ユーザの `Handler` を実行するホスト。 |
| **Handler** | ユーザが実装する Go の interface(`pkg/worker.Handler`)。実処理ロジックはここに書く。OSS の Hearth バイナリには handler は入っていない ── あなたが Hearth を import してハンドラを登録する独自 worker バイナリをビルドする。 |
| **Job kind** | ルーティング用の文字列。worker は受け付ける kind を申告し、coordinator はそれに合うジョブだけを渡す。 |
| **Bundle** | 1つの `.hearth` ファイル(tar.gz, ~1 KB)。CA 証明書、ワーカー専用の証明書+秘密鍵、Coordinator アドレスを内包。新規マシンが必要なものはこれ1つだけ。 |
| **Lease** | worker のジョブに対する一時的な所有権。ハートビートで延長される。worker が消えるとリースが切れて自動的に再キューされる。 |

## クイックスタート

前提: Go ≥ 1.26(または後述の `nix develop`)。あるいは [latest release](https://github.com/notpop/hearth/releases) からビルド済バイナリをダウンロード。

### 常時稼働ホストで(1コマンド)

```bash
hearth coordinator
```

初回実行で CA・サーバ証明書・ローカル admin bundle を自動生成し、gRPC サーバを起動。再実行時は既存のものを使い回します。

### 同じホストからジョブを投入

```bash
hearth submit --kind echo --payload "hi"
hearth status
```

CLI は `./.hearth/admin.hearth`(または `~/.hearth/admin.hearth`)を自動的に見つけるので、coordinator ホスト上では `--bundle` 指定不要。

### 別マシンを worker として追加

coordinator ホストで bundle を発行:

```bash
hearth enroll --addr <coord-ip>:7843 my-laptop    # → my-laptop.hearth(~1 KB)
```

`my-laptop.hearth` を worker ホストへ運ぶ(USB / scp / SD カード、何でも)。

worker ホストで `hearth worker --bundle my-laptop.hearth` を実行すれば接続できます ── ただし OSS バイナリは handler を持たないため、実処理を行うには `pkg/runner` を import した独自 worker バイナリをビルドしてください(下記)。

## 自分の worker をビルドする

外部プロジェクトが依存する公開 API は次の2パッケージのみ:

- `github.com/notpop/hearth/pkg/worker` — `Handler` interface
- `github.com/notpop/hearth/pkg/runner` — `RunWorker`(または細かい制御用の `Run`)

最小実装は ~15 行:

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/notpop/hearth/pkg/runner"
    "github.com/notpop/hearth/pkg/worker"
)

type myHandler struct{}

func (myHandler) Kind() string { return "my-task" }

func (myHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
    // 1. 入力を読む(in.Payload, in.Blobs[i].Open())
    // 2. 処理する。ctx.Done() を尊重する
    // 3. worker.Output{Payload, Blobs} を返す
    return worker.Output{Payload: []byte("done")}, nil
}

func main() {
    if len(os.Args) != 2 {
        log.Fatal("usage: my-worker <bundle.hearth>")
    }
    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer cancel()

    if err := runner.RunWorker(ctx, os.Args[1], myHandler{}); err != nil {
        log.Fatal(err)
    }
}
```

`runner.RunWorker` がバンドル読み込み・mTLS で coordinator に dial・worker 登録・handler ループまで全部面倒見てくれます。詳細制御(ログ、複数 handler、アドレス上書きなど)が要るなら `runner.Run` + `runner.Options` を使ってください。

完成版は `examples/img2pdf/cmd/img2pdf-worker/main.go`。

### Handler の契約

- **冪等性**: クラッシュやリース切れで Hearth は同じジョブを再配送します。「同じジョブが2回実行されても同じ結果になる(あるいは重複排除される)」ことを保証してください。
- **`ctx` を尊重する**: リースを失った時や coordinator がキャンセルを要求した時に `ctx.Done()` が立ちます。処理を止めて return すること。
- **失敗を隠さない**: error を返すと Hearth の backoff + retry が動きます。 一時的失敗は `MaxAttempts` まで自動リトライされます。
- **payload は小さく**: 数 KB 超のデータは blob (`worker.OutputBlob{Reader: ...}`) で渡してください。runtime が CAS で永続化し、SHA-256 のみ Job に乗せます。

## アーキテクチャ

Hearth は4層構造で、各層が独立してテスト可能です:

```
┌─────────────────────────────────────────────────────────────┐
│ pkg/job, pkg/worker        公開API(安定)                  │
├─────────────────────────────────────────────────────────────┤
│ internal/domain/{jobsm,retry}    pure functions, I/Oなし    │  ← 100%テスト可
├─────────────────────────────────────────────────────────────┤
│ internal/app                     オーケストレーション+ports │
│   coordinator/   workerrt/                                  │
├─────────────────────────────────────────────────────────────┤
│ internal/adapter                 I/O実装                    │
│   store/{memstore,sqlite}    blob/fs    registry/memregistry│
│   discovery/mdns             transport/grpc                 │
│   security/{pki,bundle}                                     │
└─────────────────────────────────────────────────────────────┘
                                   ↑
                               cmd/hearth/ が
                               すべてを束ねる
```

これを崩さない規律:

- `internal/domain/*` は `internal/adapter/*` を一切 import しない。`time.Now`、`context`、I/O も使わない。時刻と乱数は引数で受ける。
- `internal/app/*` は domain と、`internal/app/ports.go` で宣言した interface(`Store` / `BlobStore` / `Clock` / `WorkerRegistry`)のみに依存する。
- 実装(adapter)は `internal/adapter/*` にあり、`cmd/` レイヤだけがそれらを束ねる。

これにより SQLite ストアと in-memory のテスト用 fake が**オーケストレーションを1行も書き換えずに差し替え可能**になっています。

### ストレージ

- **ジョブ**: SQLite (WAL モード、`modernc.org/sqlite` で cgo なし)。`*sql.DB.MaxOpenConns=1` で書き込みを直列化し、`SQLITE_BUSY` リトライを排除。pragma は `journal_mode=WAL`, `synchronous=NORMAL`, `busy_timeout=5000`。LAN 規模の負荷なら十分。`app.Store` interface のおかげで BadgerDB 等にも切り替え可能。
- **Blob**: ファイルシステム CAS、`<data>/blobs/<sha[:2]>/<sha>` に rename で原子的に書き込み。同じ内容は重複排除される。MB 級データは必ず blob 経由(SQLite payload 列に詰めない)。

### セキュリティ

- **全通信 mTLS**: クライアント(worker, CLI)もサーバも家の CA で署名された証明書を提示。TLS 1.3 のみ、Ed25519 鍵。
- **CAルート**: `hearth ca init` で自己署名 CA を生成、coordinator から外に出ない。worker 証明書は CA から都度発行。
- **1ファイル登録**: `hearth enroll <name>` が `<name>.hearth`(tar.gz: CA証明書 + クライアント証明書 + 秘密鍵 + Coordinator アドレス)を出力。信頼できる物理メディアで運ぶ。

### 発見

mDNS サービス `_hearth._tcp.local`。Coordinator がホスト+ポートを広告し、bundle に明示アドレスがない場合 worker は自動発見にフォールバック。ルータ設定不要、外部 DNS 不要。

#### 注意: WiFi と有線の橋渡し

家庭用ルータでは WiFi と有線が同一 L2 ドメインで bridge されているため、**ユニキャスト通信(gRPC over TCP)は問題なく通ります**。ただし mDNS は multicast 依存なので、ルータの「AP Isolation」が ON だったり、ゲストネットワーク経由だったりすると届かないことがあります。その場合は bundle の `--addr` に固定 IP を入れれば確実です。

### 障害ハンドリング

- **worker がジョブ実行中にクラッシュ**: ハートビート停止 → リース TTL 経過 → coordinator の reclaim sweeper がジョブを再キュー → 別の worker が拾う。クラッシュした試行は `MaxAttempts` の1カウントとして数えられ、設定された backoff が適用される。
- **coordinator がクラッシュ**: SQLite WAL のおかげで commit 済みジョブは失われない。再起動後の最初の sweep でリース切れジョブを回収。
- **ネットワーク分断**: worker は自然と一時停止(long-poll Lease がエラー、backoff で再試行)。回復後、分断を生き延びなかったリースは回収される。worker が再接続したジョブはハートビートで継続。

## 開発

### 再現可能な環境

リポジトリには Nix flake が同梱されています。Nix がインストール済みなら:

```bash
nix develop          # Go 1.26.2, gopls, golangci-lint, sqlite, protoc 入りシェル
go test ./...
```

direnv 派なら `direnv allow` で同じ環境に入れます。

### よく使うコマンド

```bash
go test ./...               # ユニット+結合テスト(全体 ~10秒)
go test -race ./...         # データレース検出
go test -cover ./...        # カバレッジ
go vet ./...
go build ./cmd/hearth       # 1バイナリ CLI
go build ./examples/img2pdf/cmd/img2pdf-worker

# .proto を編集後、gRPC スタブを再生成:
protoc --proto_path=api/proto \
  --go_out=. --go_opt=module=github.com/notpop/hearth \
  --go-grpc_out=. --go-grpc_opt=module=github.com/notpop/hearth \
  api/proto/hearth/v1/hearth.proto
```

### レイアウト

上記アーキテクチャ図参照。一行で言うと: `pkg/` が公開、`internal/domain/` が pure ロジック、`internal/app/` がオーケストレーション、`internal/adapter/` が I/O、`cmd/` が結線、`examples/` が利用例。

## ロードマップ(未実装)

- [ ] `CancelJob` のエンドツーエンド実装(現在 server 側は Unimplemented)
- [ ] `WatchJob` の push 通知(現在は server 側 poll)
- [ ] CLI `submit` での blob 入力(`--blob <path>`)
- [ ] worker の自動再接続(指数 backoff)
- [ ] Coordinator HA(現状は単一 coordinator、家庭規模では十分)

## ライセンス

[MIT](./LICENSE) © 2026 notpop
