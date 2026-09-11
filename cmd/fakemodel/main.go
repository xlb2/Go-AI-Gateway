// Command fakemodel 起一个本地假模型服务器（OpenAI 兼容），把 .env 里的
// VOLC_BASE_URL 指过来即可让整个 agent 链路跑在确定性场景上，不烧 token、不依赖网络。
//
//	go run ./cmd/fakemodel                                  # 内置场景
//	go run ./cmd/fakemodel -scenario test/fakemodel/scenarios/default.json
//
// 和 cmd/verify、cmd/probe 是同一类东西：本地开发/验证用的小工具。
package main

import (
	"flag"
	"log"
	"net/http"
	"os"

	"go_im_gateway/test/fakemodel"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9099", "监听地址")
	scenarioPath := flag.String("scenario", "", "场景 JSON 路径（留空用内置场景）")
	flag.Parse()

	if *scenarioPath == "" {
		*scenarioPath = os.Getenv("FAKEMODEL_SCENARIO")
	}

	sc := fakemodel.DefaultScenario()
	if *scenarioPath != "" {
		loaded, err := fakemodel.LoadScenario(*scenarioPath)
		if err != nil {
			log.Fatalf("加载场景失败: %v", err)
		}
		sc = loaded
		log.Printf("已加载场景: %s", *scenarioPath)
	} else {
		log.Printf("使用内置场景")
	}

	srv := fakemodel.NewServer(sc)
	log.Printf("假模型已启动: http://%s/api/v3", *addr)
	log.Printf("把 .env 里改成 VOLC_BASE_URL=http://%s/api/v3 即可（业务代码不用改）", *addr)
	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
