---
name: analysis-script-spec
description: 分析脚本修改规范总入口：修改 / 补充脚本时必须遵守的约定，目前覆盖输出约定（读 get_script 返回的 io_schema_path 中的 io_schema.json，按 outputs 的 name / type / role 在 script_file_path 的脚本里产出文件，并在 params$output_dir 下写出 outputs.json），其余约定后续补充。用户要求修改脚本、补充输出或节点提示缺少产物时使用。
user-invocable: true
---

## 分析脚本修改规范

本技能是「修改分析脚本」时的规范总入口。当前只写了输出约定，其余规范后续逐条补充，
补充时新增小节即可，不要改动已有小节的含义。

### 通用要求

- 只改 `script_file_path` 指向的脚本；脚本输入输出契约以 `io_schema_path` 指向的 io_schema.json 为准，
  不要在脚本里发明与契约不一致的参数名或产物名。
- 代码尽量简单：能用直接写法就不用函数封装；路径一律由参数拼出，不硬编码。
- 修改脚本后必须调用 `run_analysis_node` 重新运行，返回 success 才算完成；失败先看 log 再决定怎么改。

### 输出约定（已补充）

#### 1. 先读 io_schema.json 拿到 outputs

- 调用 `get_script`（需要 script_id），用返回结果中的 `io_schema_path` 读取这份 io_schema.json；
  节点上下文里已给出 io_schema.json 时也可直接查看。
- 只看 `outputs` 数组，逐项确认：
  - `name`：产物名，也是 outputs.json 的键名；
  - `type`：产物类型（通常为 file）；
  - `role`：用途（如 TABLE / FIGURE），决定产物格式倾向。
- `outputs` 为空数组时，脚本不需要写 outputs.json。

#### 2. 按 outputs 修改 script_file_path 的脚本

- `outputs` 有几项，脚本就要产出几项；产物路径一律拼在 `params$output_dir` 下，不要硬编码路径。
- 产物格式与 `type` / `role` 对应：`role: TABLE` 用 tsv / csv，`role: FIGURE` 用 png / pdf。
- 产物写入文件即可；qmd 中需要展示的表格仍用 `knitr::kable()`。

如果 io_schema.json 中 `outputs` 有值，qmd 中需要添加输出，例如 io_schema.json 里：

```json
"outputs": [
  {
    "name": "tsv",
    "type": "file",
    "role": "TABLE"
  }
]
```

对应 qmd 中需要写出 outputs.json（键名与 io_schema.outputs 的 name 一致，值指向实际产物路径）：

```r
outputs <- list(
  tsv = mutate_genes_path
)
jsonlite::write_json(outputs, file.path(params$output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
```

多个输出时按 `name` 逐个补齐：

```r
outputs <- list(
  tsv = mutate_genes_path,
  plot = volcano_plot_path
)
jsonlite::write_json(outputs, file.path(params$output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
```

#### 3. 检查要点

- outputs.json 的键名必须与 `io_schema.outputs[].name` 完全一致（大小写、下划线都不能改），
  多写或少写都会导致产物校验失败。
- 值写实际产物路径（由 `params$output_dir` 拼出的路径），不要写相对路径。
- `outputs` 为空数组时不写 outputs.json。

### 待补充

- 输入读取约定（params.json 键名、表格读取方式）。
- 非 R 语言脚本（Python / shell）的对应写法。
- 目录类输出、多个同类产物、可选输出的约定。
- 依赖声明与可复现性约定。
