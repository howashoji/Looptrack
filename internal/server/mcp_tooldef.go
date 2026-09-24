package server

import (
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/howashoji/looptrack/internal/i18n"
)

// MCP のツール定義（ツールの説明と入力項目の説明）を、接続の言語で出す。
//
// 言語ごとに *mcp.Server を 1 つずつ作り（mcpHandler）、ツールはその言語の文面で登録する。
// ツールの説明は登録のときに i18n.T で引けば済むが、入力項目の説明は SDK が構造体の jsonschema タグから
// 反射で作る。タグは静的な文字列なので、言語ごとに違う値を置けない。そこで:
//
//   - タグには文面ではなく**対訳表のキー**（server.mcp.arg.…）を書く。タグの値は反射でそのまま
//     description になる（jsonschema-go）。
//   - 登録の前にスキーマを自分で作り（jsonschema.For）、キーの description を i18n.T で置き換えてから
//     Tool.InputSchema に渡す。SDK は InputSchema が渡されていれば反射で作り直さない（タグを読まない）。
//
// タグに日本語の正本を残して英語だけを表から引く形は採らない。正本が 2 か所（タグと ja.json）になり、
// 片方だけ直すと黙って食い違う。キーにしておけば、タグは i18n の lint（lint_test.go）がキーとして集めるので、
// 表に無いキー・どこからも使われないキーは組み立ての時点で落ちる。
// タグの値に = を含めない（jsonschema-go は先頭の語に = を含むタグを予約語として拒む）。キーはドット区切りなので当たらない。
//
// スキーマは言語ごとに作り直す（jsonschema.For は呼ぶたびに新しい値を返す）。同じ値を 2 つのサーバで
// 共有すると、片方の言語で書き換えたものがもう片方に出る。

// mcpArgKeyPrefix は入力項目の説明に使う対訳表のキーの接頭辞。
const mcpArgKeyPrefix = "server.mcp.arg."

// addTool は、入力項目の説明を lang で引いたスキーマを付けてツールを登録する。
// ツールの説明（t.Description）は呼ぶ側が i18n.T(lang, "server.mcp.tool.…") で入れる
// （ID を文字列リテラルで書くと、i18n の lint が表との過不足を見られる）。
func addTool[In, Out any](srv *mcp.Server, lang i18n.Lang, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		// 入力の型は組み立ての時点で決まっている。作れないのは実装の誤りなので、SDK の AddTool と同じく起動時に落とす。
		panic(fmt.Sprintf("MCP tool %s: input schema: %v", t.Name, err))
	}
	localizeSchema(lang, schema)
	t.InputSchema = schema
	mcp.AddTool(srv, t, h)
}

// localizeSchema は、スキーマの中のキーの description を lang の文面に置き換える（入れ子の項目・配列の要素も）。
func localizeSchema(lang i18n.Lang, s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if strings.HasPrefix(s.Description, mcpArgKeyPrefix) {
		// i18n.T は ID を文字列リテラルで受ける決まり（lint_test.go）。ここの ID はタグから来るので Msg で引く
		// （タグの ID は lint_test.go がタグから集めて、表との過不足を見る）。
		s.Description = i18n.Msg{ID: s.Description}.In(lang)
	}
	for _, p := range s.Properties {
		localizeSchema(lang, p)
	}
	localizeSchema(lang, s.Items)
	localizeSchema(lang, s.AdditionalProperties)
}
