# 使い方ガイド

[English](./USAGE.md) · [プロジェクト README](../README.ja.md)

README より深い、API・統合パターン・契約事項の詳細リファレンス。

---

## 目次

1. [公開 API の全体像](#公開-api-の全体像)
2. [ユースケース](#ユースケース)
   - [A. 自分の worker をビルドする](#a-自分の-worker-をビルドする)
   - [B. ジョブをプログラマブルに投入する](#b-ジョブをプログラマブルに投入する)
   - [C. 1バイナリで worker と submitter を兼ねる](#c-1バイナリで-worker-と-submitter-を兼ねる)
   - [D. CLI のみ(Go コードなし)](#d-cli-のみgo-コードなし)
3. [Handler 契約](#handler-契約)
4. [Spec デフォルト値](#spec-デフォルト値)
5. [エラーハンドリング](#エラーハンドリング)
6. [進捗報告](#進捗報告)
7. [Blob I/O のパターン](#blob-io-のパターン)
8. [トラブルシューティング](#トラブルシューティング)

---

## 公開 API の全体像

外部 Go プロジェクトが依存できるのは**以下の4パッケージのみ**:

| パッケージ | 用途 |
|---|---|
| `github.com/notpop/hearth/pkg/job` | ドメイン型(Job, Spec, State, BlobRef, ...)。データのみ、ロジックは持たない。 |
| `github.com/notpop/hearth/pkg/worker` | `Handler` interface + `Input` / `Output`。**ハンドラを書く側**。 |
| `github.com/notpop/hearth/pkg/runner` | worker プロセスを動かす: `RunWorker(ctx, bundlePath, handlers...)`。 |
| `github.com/notpop/hearth/pkg/client` | 外部サービスから coordinator に接続: submit / watch / blob I/O。 |

`internal/` 配下は本モジュール専用、外部から import 不可(Go 言語仕様で禁止)。「これが欲しい」があれば issue を立ててください — 「pkg/ に昇格すべきだね」または「公開済みの代替案はこれだよ」を返します。

---

## ユースケース

### A. 自分の worker をビルドする

家のマシンが拾って実行するハンドラを書きたい。

最小実装(~15 行):

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

詳細制御が必要なら `runner.Run(ctx, runner.Options{...})`:

```go
err := runner.Run(ctx, runner.Options{
    BundlePath:      "/path/to/worker.hearth",
    Handlers:        []runner.Handler{myHandler{}, otherHandler{}},
    AddrOverride:    "192.168.1.10:7843", // bundle のアドレスを上書き
    Version:         "my-worker/1.0",
    LeaseTTL:        60 * time.Second,
    PollTimeout:     30 * time.Second,
    HeartbeatPeriod: 15 * time.Second,
    Logger:          slog.New(slog.NewJSONHandler(os.Stderr, nil)),
})
```

複数 handler の同居 OK。`Kind()` は重複不可。

### B. ジョブをプログラマブルに投入する

Web API、Slack bot、cron 等の Go サービスから CLI を経由せずに投入したい。

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/notpop/hearth/pkg/client"
    "github.com/notpop/hearth/pkg/job"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    c, err := client.Connect(ctx, "/path/to/admin.hearth")
    if err != nil { log.Fatal(err) }
    defer c.Close()

    id, err := c.Submit(ctx, job.Spec{
        Kind:        "img2pdf",
        Payload:     []byte(`{"output":"album.pdf"}`),
        MaxAttempts: 3,
        LeaseTTL:    5 * time.Minute,
    })
    if err != nil { log.Fatal(err) }
    log.Printf("submitted: %s", id)

    // ジョブ完了まで監視。terminal でストリームが終わる。
    err = c.Watch(ctx, id, func(j job.Job) {
        log.Printf("[%s] %s", j.UpdatedAt.Format("15:04:05"), j.State)
        if j.Progress != nil {
            log.Printf("  進捗: %.0f%% — %s", j.Progress.Percent*100, j.Progress.Message)
        }
    })
    if err != nil { log.Fatal(err) }
}
```

ファイル入力は blob 経由:

```go
f, _ := os.Open("photo.png")
defer f.Close()

ref, _ := c.PutBlob(ctx, f) // coordinator の CAS にストリーム

id, _ := c.Submit(ctx, job.Spec{
    Kind:  "img2pdf",
    Blobs: []job.BlobRef{ref},
})
```

### C. 1バイナリで worker と submitter を兼ねる

ジョブを生産しつつ消費もするケース(ファンアウトサービス等)。両方 import すれば OK:

```go
import (
    "github.com/notpop/hearth/pkg/client"
    "github.com/notpop/hearth/pkg/runner"
    "github.com/notpop/hearth/pkg/worker"
)

func main() {
    // submit 側
    c, _ := client.Connect(ctx, "/admin.hearth")
    defer c.Close()
    go submitLoop(c)

    // worker 側
    _ = runner.RunWorker(ctx, "/worker.hearth", subHandler{c: c})
}
```

ハンドラから client を経由して子ジョブを投げる:

```go
func (h subHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
    pages := splitWork(in.Payload)
    for _, p := range pages {
        h.c.Submit(ctx, job.Spec{Kind: "img2pdf-page", Payload: p})
    }
    return worker.Output{}, nil
}
```

### D. CLI のみ(Go コードなし)

`hearth` バイナリを直接利用。詳しくは README の[CLI リファレンス](../README.ja.md#cli-リファレンス)。シェル経由のプログラマブル統合も可能:

```sh
JOB_ID=$(hearth submit --kind img2pdf --blob photo.png)
hearth status --job "$JOB_ID" --watch
```

動くが脆い(出力 parse とエラーハンドリング)。本番統合は pkg/client を推奨。

---

## Handler 契約

すべての Handler に共通する規則:

- **冪等性**: クラッシュ・lease 切れ・ネットワーク分断で Hearth は同じ Job を再配送します。同一 ID で2回実行されても**同じ結果**(または重複排除された結果)になること。
- **`ctx` を尊重する**: lease を失った時や coordinator がキャンセル要求した時に `ctx.Done()` が立ちます。処理を止めて return すること。中断した試行は失敗扱いですが、ジョブ自体が terminal なのでリトライはされません。
- **失敗は error で返す**: 設定された backoff に従って `Spec.MaxAttempts` までリトライされます。`nil` を返せば成功扱い(`Output.Payload` が空でも)。
- **永続ブロックしない**: 長時間処理は `in.Report` を定期的に呼んで進捗を出してください。lease TTL は heartbeat で自動延長されるので意識する必要はありません。
- **同一 handler instance がゴルーチンから並行に呼ばれる**: ジョブ毎の状態は handler の receiver ではなくローカル変数に持つこと。

---

## Spec デフォルト値

`job.Spec` は最小形 `Spec{Kind: "k"}` で動くように設計。各フィールドのゼロ値挙動:

| フィールド | ゼロ値の扱い |
|---|---|
| `Kind` | 必須(`^[a-z0-9._-]+$`、≤ 64文字) |
| `Payload` | 空 byte slice |
| `Blobs` | なし |
| `MaxAttempts` | 0 → 3 回。1 = リトライなし。負値 = 無制限。 |
| `LeaseTTL` | 0 → 30 秒 |
| `Backoff` | ゼロ値 → `{1s, 60s, ×2, jitter 0.1}`。部分指定なら個別フォールバック。 |

個別オーバーライド例:

```go
job.Spec{
    Kind:        "video-encode",
    MaxAttempts: 1,                        // リトライなし
    LeaseTTL:    30 * time.Minute,         // 長時間ジョブ
    Backoff:     job.BackoffPolicy{Multiplier: 1}, // 定数 backoff (Initial=1s、Max=60s はデフォルト)
}
```

---

## エラーハンドリング

`pkg/client` は sentinel error を提供。`errors.Is` で判定:

```go
import "github.com/notpop/hearth/pkg/client"

j, err := c.Get(ctx, id)
switch {
case errors.Is(err, client.ErrNotFound):
    // HTTP ハンドラの 404
case errors.Is(err, client.ErrInvalidArgument):
    // 400 — bundle は OK、リクエストが不正
case errors.Is(err, client.ErrInvalidTransition):
    // ジョブが既に terminal、no-op として扱う
case errors.Is(err, client.ErrUnauthenticated):
    // bundle 再発行、証明書が拒否された
case err != nil:
    // 未知 / トランスポート系 — backoff してリトライ
default:
    // 成功
}
```

元の error は wrap されたまま残るので `err.Error()` には coordinator のメッセージも含まれます。

handler では普通の error を返すだけでリトライポリシーが発動。`err.Error()` が `Job.LastError` に記録されます。特殊なエラー型は不要。

---

## 進捗報告

長時間 handler は `in.Report(percent, message)` を呼んで進捗を流します。最新の値が勝ち、タイトループから呼んでも安全。

```go
func (h myHandler) Handle(ctx context.Context, in worker.Input) (worker.Output, error) {
    pages := decodePages(in.Payload)
    for i, p := range pages {
        in.Report(float64(i)/float64(len(pages)), fmt.Sprintf("page %d/%d", i+1, len(pages)))
        if err := process(ctx, p); err != nil {
            return worker.Output{}, err
        }
    }
    in.Report(1.0, "done")
    return worker.Output{}, nil
}
```

内部の動き:
- Report は atomic に保存され、毎回ワイヤーに乗らない
- 次の heartbeat(通常 10秒毎)で最新値を 1 回だけ送る
- Watch 購読者には `Job.Progress` 経由で届く
- `hearth status` と `hearth status --watch` が percent + message を表示

`in.Report` は `nil` の場合あり(unit test 等で `Input` を直接組み立てたとき)。両用したいなら `if in.Report != nil { ... }` でガードしてください。

---

## Blob I/O のパターン

### 入力

入力 blob は遅延的に展開される — `in.Blobs[i].Open()` が coordinator からのストリームを返します。**必ず Close** すること。

```go
for _, b := range in.Blobs {
    rc, err := b.Open()
    if err != nil { return worker.Output{}, err }
    defer rc.Close()
    // rc から読む
}
```

### 出力

任意の `io.Reader` をラップ。runtime が SHA-256 を計算しながら coordinator に流します。

```go
out := worker.Output{
    Blobs: []worker.OutputBlob{{
        Reader: someReader, // *bytes.Reader, *os.File, etc.
        Size:   sizeHintInBytes, // 任意、進捗 UI 用
    }},
}
```

冪等性: 同じバイト列なら同じ `BlobRef` が返ります。2 回試行で全く同じ出力が出れば、2 回目の `PutBlob` は既存 ref を返して再保存しません。

---

## トラブルシューティング

### "rpc error: code = Unavailable, transport: authentication handshake failed"

bundle 内の CA が coordinator の起動時 CA と一致しない:

- bundle 発行後に `hearth ca init` を再実行した(CA ディレクトリ削除等)
- 別 coordinator 用の bundle を使ってる(dev CA を本番に向けた等)

対処: 現 CA で bundle を再発行(`hearth enroll <name>`)。

### `hearth submit` が "no coordinator address" と言う

bundle に `CoordinatorAddrs` が無いか(`--addr` 付きで再発行)、admin bundle が自動探索パス(`./.hearth/admin.hearth`, `~/.hearth/admin.hearth`) に無い。`--bundle` か `--coordinator <ip>:7843` で明示。

### worker がジョブを拾わない

確認:
1. worker が接続済か: `hearth nodes` に出る
2. worker が正しい kind を申告しているか: `hearth nodes` の `KINDS` 列
3. ジョブの `Kind` と worker の `Kind()` が**完全一致**(大文字小文字、空白厳密)

submit 時に Kind バリデーションで弾かれていれば、`ErrInvalidArgument` のメッセージに正規表現ヒントが含まれます。

### WiFi と有線の間で mDNS が届かない

家庭用ルータの大半は L2 ブリッジしているので mDNS は素通り。AP isolation が ON だと届かない場合があり、その時は静的 IP を bundle に埋める:

```sh
hearth enroll --addr 192.168.1.10:7843 my-worker
```

coordinator 側で mDNS 配信を OFF にする:

```sh
hearth coordinator --mdns=false
```
