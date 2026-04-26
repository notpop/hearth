# Hearth

> 家庭内ネットワーク向け分散タスクランナー。シングルバイナリ、デフォルト mTLS、ワーカー登録は1ファイル。

[English README](./README.md)

Hearth は1台のホストでジョブをキューに積み、家にある別マシン(Mac, Windows, Linux, NAS など)が LAN 経由で拾って処理するための仕組みです。例えば「画像→PDF変換」「動画エンコード」「バッチ ML 推論」のような重い処理に。親と子の役割は OS を問わず任意の組み合わせで選べます。LAN を出る通信はゼロ。

**状態: Alpha**。 wire プロトコル・公開 API・bundle フォーマットは安定前。利用する場合は commit を pin してください。

## 用語

| 用語 | 意味 |
|---|---|
| **Coordinator** | キュー所有・blob 保管・gRPC API を提供する**1台**のホスト。 |
| **Worker** | 1つ以上の *kind* のジョブを取りに行き、ユーザの `Handler` を実行するホスト。 |
| **Handler** | ユーザが実装する Go の interface(`pkg/worker.Handler`)。実処理ロジックを書く。 |
| **Bundle** | 1つの `.hearth` ファイル(~1 KB)。CA 証明書 + クライアント証明書 + 秘密鍵 + Coordinator アドレスを内包。 |

## クイックスタート

[最新リリース](https://github.com/notpop/hearth/releases) からバイナリを取得、または `go build ./cmd/hearth`。あとは:

```sh
# 常時稼働ホストで(初回起動で CA + admin bundle を自動生成):
hearth coordinator

# 同じホストからジョブを投入(CLI が admin bundle を自動発見):
hearth submit --kind echo --payload "hi"
hearth status
```

### 別マシンを worker として追加

```sh
# Coordinator ホストで:
hearth enroll --addr <coord-ip>:7843 my-laptop      # → my-laptop.hearth(~1 KB)

# my-laptop.hearth を worker ホストへ運ぶ(USB / scp / SD カード)
# Worker ホストで、自前の handler 入りバイナリを起動(下記):
my-worker my-laptop.hearth
```

OSS の `hearth worker` コマンドは bundle を読み込んで接続確認はできますが handler を持ちません。実処理を行うには `pkg/runner` を import した独自 worker バイナリをビルドします。

## 自分の worker をビルドする

> 詳細: [docs/USAGE.ja.md](./docs/USAGE.ja.md)

外部プロジェクトが依存する公開 API は次の4パッケージのみ:

- `github.com/notpop/hearth/pkg/worker` — `Handler` interface(worker 側)
- `github.com/notpop/hearth/pkg/runner` — `RunWorker`(細かい制御は `Run` + `Options`)
- `github.com/notpop/hearth/pkg/client` — プログラマブル投入(Web app, bot, ...)
- `github.com/notpop/hearth/pkg/job`    — 上記すべてが共有するドメイン型

最小実装は ~15 行:

```go
package main

import (
    "context"; "log"; "os"; "os/signal"; "syscall"

    "github.com/notpop/hearth/pkg/runner"
    "github.com/notpop/hearth/pkg/worker"
)

type myHandler struct{}

func (myHandler) Kind() string { return "my-task" }

func (myHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
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

`runner.RunWorker` がバンドル読み込み・mTLS で coordinator に dial・worker 登録・handler ループまで全部面倒見てくれます。完成版は `examples/img2pdf/cmd/img2pdf-worker/main.go`。

### Handler の契約

- **冪等性**: クラッシュやリース切れで Hearth は同じジョブを再配送します。
- **`ctx` を尊重する**: リースを失った時や coordinator がキャンセルを要求した時に `ctx.Done()` が立ちます。処理を止めて return すること。
- **失敗を error で返す**: Hearth がジョブ設定の backoff/再試行を `MaxAttempts` まで適用します。
- **大きなペイロードは blob に**: `worker.OutputBlob{Reader: ...}` を使えば runtime が CAS で永続化し、SHA-256 のみ Job に乗ります。

## CLI リファレンス

```
hearth coordinator [--listen ...] [--data ...] [--ca ...] [--mdns]
hearth enroll      [--addr <host:port>] [--out <path>] [--validity <dur>] <name>
hearth submit      [--bundle <path>] --kind <k> [--payload <s>] [--blob <path> ...]
hearth status      [--bundle <path>] [--job <id>] [--watch] [--limit N]
hearth cancel      [--bundle <path>] <job-id>
hearth nodes       [--bundle <path>]
hearth ca init     [--dir <path>] [--name <cn>]
hearth worker      --bundle <path>     # 接続確認のみ、handler なし
hearth version
```

`--bundle` は省略すると `$HEARTH_BUNDLE`、`./.hearth/admin.hearth`、`~/.hearth/admin.hearth` の順に自動探索されます。Coordinator ホスト上ではフラグ不要。

## ネットワークの注意

Hearth は完全に LAN 内で動作します。通信は mTLS な gRPC のみ、サービス発見は mDNS(`_hearth._tcp.local`)。

一般的な家庭用ルータは WiFi と有線を L2 でブリッジしているので、Mac (WiFi) と Windows (有線) が同じ Coordinator に問題なく届きます。mDNS multicast もデフォルト設定なら通ります。AP Isolation を有効化していたりゲストネットワーク経由だったりすると mDNS が届かない場合があるので、`hearth enroll --addr <ip>:7843` で IP を明示すれば確実です。

## 開発

```sh
nix develop          # Go 1.26 + ツール一式(flake.nix で固定)
just                 # タスク一覧
just test            # テスト一式(~10 秒)
just build           # ./bin/hearth
```

`pkg/` が公開 API。それ以外は `internal/` 配下で、変更可能性があります。

## ライセンス

[MIT](./LICENSE) © 2026 notpop
