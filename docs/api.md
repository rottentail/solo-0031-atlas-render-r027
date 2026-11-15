# HTTP 协议

所有业务路径以 `/v1` 开头。请求体使用 JSON，素材上传直接使用 PNG 字节。

| 方法和路径 | 请求 | 结果 |
|---|---|---|
| PUT /scenes/{id} | name, expectedRevision, document | 完整场景，200 |
| GET /scenes/{id} | 无 | 完整场景，200 |
| POST /scenes/{id}/state | expectedRevision, state | 完整场景，200 |
| POST /scenes/{id}/snapshots | expectedRevision | 快照和素材闭包，201 |
| PUT /assets/{id} | PNG字节 | 素材及base64字节，201 |
| POST /assets/{id}/retire | 无 | state，200 |
| GET /snapshots/{id} | 无 | 快照，200 |
| POST /snapshots/{id}/revoke | 无 | state，200 |
| GET /snapshots/{id}/package | 无 | atlas-scene交换包，200 |
| POST /imports/{id} | name, package | 新场景，201 |
| POST /jobs | snapshotId, scale；请求头Idempotency-Key | 任务，202 |
| GET /jobs/{id} | 无 | 任务，200 |
| POST /jobs/{id}/cancel | 无 | 任务，200 |
| POST /jobs/{id}/retry | 无 | 任务，202 |
| GET /artifacts/{id} | 可选If-None-Match | PNG，200或304 |
| DELETE /artifacts/{id} | 无 | state，200 |
| POST /compare | left, right（结果ID） | 像素比较，200 |
| GET /status | 无 | 场景/素材/快照/任务计数和结果字节数 |
| GET /events | 无 | 最多500个有序领域事件 |

`GET /healthz` 不带 `/v1`，返回ready。它表明进程已打开并恢复状态，不包含单独的磁盘可写探测。任务响应省略固定输入的有效载荷，输入细节通过快照接口读取。

文档字段：width、height、background、layers。每个图层有id、kind、width、height、transform、opacity、fill；transform包括x、y、scaleX、scaleY、degrees。图层可以有clip（x/y/width/height），group使用children，polygon使用points，image使用assetId。solid颜料有color；linear还包括from、to，并支持两种等价的颜色声明。示例是可直接提交的完整请求。

linear 颜料的两种形式：

- 传统两端形式：`color` 和 `endColor` 给出两个不透明端点，语义与早期版本完全一致，渲染结果逐字节不变。
- 多停靠点形式：`stops` 给出 2–32 个停靠点，每个停靠点含 `position`（0–1）、`color`（#RRGGBB）和 `alpha`（0–1）。使用 stops 时不得再提供 color/endColor。停靠点可任意顺序提交，服务按 position 稳定排序；同一 position 的多个停靠点形成确定性硬色带，边界处取文档顺序中靠后的停靠点，边界两侧分别向各自相邻停靠点过渡。超出 0–1 的投影位置按最外侧停靠点延伸。

`interpolation` 仅在多停靠点形式下可用，取值为 `rgb`（默认，编码 sRGB 空间）或 `linear-rgb`（线性光空间，过渡更符合人眼对明暗的感受）。所有颜色过渡在预乘 alpha 空间进行：向 alpha 0 淡出时只降低透明度，不会把该停靠点不可见的 RGB 值混入成暗边；alpha 为 0 处输出零 RGB。同一份快照在任何实例上按相同浮点规则产生相同 PNG。

幂等键长度1–80，使用英文字母、数字、下划线和连字符，首位不能是后两者。它在服务实例持久状态中全局生效，不随任务完成、取消、结果清理或进程重启失效。改换请求语义需使用新键。

服务接受相同内容的素材重复上传，退役素材不接受重新上传。交换包导出保留快照状态；导入会创建全新的active场景，原快照不作为目标服务中的快照记录落地。导入目标ID必须未使用；共享素材必须内容相同且仍可用。
