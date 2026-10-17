---
name: analysis-node-script-editing
description: 在已创建的分析节点里撰写 / 修改 qmd 分析脚本：从 script_file_path 与 params.json 编写最简代码、tidyverse 读取表格、knitr::kable 打印、按 io_schema outputs 写 outputs.json，以及用 run_analysis_node 运行并迭代。用户要求在分析节点中写代码、改代码或运行当前节点时使用。
user-invocable: true
---

## 分析节点脚本修改

基于 script_file_path 与 params.json 中的内容，根据用户要求完成代码撰写：使用最简单的代码，
尽量不使用函数；图片直接在 qmd 中打印；不要读取其他文件，只需要修改 script_file_path 文件中的内容。

- 读取 tsv / csv 时使用 tidyverse；无需确认查找 R 包（用户手动安装），只需保证代码简洁清楚即可。
- qmd 中打印表格需要使用 `knitr::kable()`。

### 运行当前节点

- 当用户说「运行当前节点 / 跑一下这个节点」时，调用 run_analysis_node 工具，analysis_node_id 留空即可运行当前上下文节点。
- 用户给出其它节点 ID 时，把该 ID 原样传给 analysis_node_id。
- 工具会阻塞等待节点进入终态，返回 status / success / log；失败时先看 log 再判断是否需要修改脚本重跑。
- 修改代码后总是需要调用 run_analysis_node 重新运行分析。

### 输出约定

如果 io_schema.json 中 `outputs` 有值，qmd 中需要添加输出，例如 io_schema.json 里：

~~~json
"outputs": [
  {
    "name": "tsv",
    "type": "file",
    "role": "TABLE"
  }
]
~~~

对应 qmd 中需要写出 outputs.json（键名与 io_schema.outputs 的 name 一致，值指向实际产物路径）：

~~~r
outputs <- list(
  tsv = mutate_genes_path
)
jsonlite::write_json(outputs, file.path(params$output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
~~~
