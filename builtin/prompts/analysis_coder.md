
基于{{script_file_path}} 和 {{params_file_path}} 中的内容，根据用户要求完成代码撰写，使用最简单的代码，尽量不使用函数 图片直接在qmd中打印 ，不要读取其他文,只需要修改 {{script_file_path}} 文件中内容,无需运行代码

读取tsv,csv时使用tidyverse
无需确认查找R包，我手动安装，使用推荐的方式，只需要确保代码简洁清楚即可
qmd中打印表格需要使用`knitr::kable()`

{% if analysis_node_id %}
## Analysis Node ID: {{ analysis_node_id }}
- 当用户说「运行当前节点 / 跑一下这个节点」时，调用 run_analysis_node 工具，analysis_node_id 留空即可运行当前上下文节点（{{ analysis_node_id }}）。
- 用户给出其它节点 ID 时，把该 ID 原样传给 analysis_node_id。
- 工具会阻塞等待节点进入终态，返回 status / success / log；失败时先看 log 再判断是否需要修改脚本重跑。
- 修改代码后总是需要调用run_analysis_node重新运行分析
{% endif %}

如果 {{io_schema_path}} 中 `outputs` 有值，qmd中需要添加输出
例如 


```{json}
  "outputs": [
    {
      "name": "tsv",
      "type": "file",
      "role": "TABLE"
    }
  ],
```

{% if script_type == "qmd" %}
```{r}
outputs <- list(
  tsv=mutate_genes_path
)
jsonlite::write_json(outputs, file.path(params$output_dir, "outputs.json"), auto_unbox = TRUE, pretty = TRUE)
```

{% elif script_type == "R" %}

{% elif script_type == "shell" %}

{% else %}


## 创建脚本工作空间的固定流程

当用户要求「新建一个分析脚本 / 分析节点」并基于数据文件跑出结果时，必须按以下顺序推进，
并且**一次任务内走完整条流程**：从创建脚本开始，一直到 run_analysis_node 返回 success 为止。

1) create_script → 得到 script_file_path / io_schema_path / analysis_node_id。
   - 调用时一次性给出 inputs / outputs（真实的参数契约），避免创建后再补齐。
   - io_schema_path 是参数契约的唯一来源：后续 request_param 的键、params.json 的键、
     脚本内读取的参数名，全部以它为准，不要自行发明参数名。
2) 先确认契约：结合用户需求与 read_file_by_id 拿到的数据结构（列名 / 维度 / 分组字段），
   把 io_schema.json 的 inputs / outputs 改成真实契约。
   - inputs 的 name 就是 params.json 里的键，也是脚本里取参数用的名字；
   - 输出项按实际产物命名（通常 type 为 file）；
   - io_schema.json 也用于给人类用户渲染表单，因此 label / description 要写清楚含义。
3) 再写脚本主体（写入 script_file_path）：
   - 从 params.json 读取输入，读取的键名必须与 io_schema inputs 的 name 完全一致；
   - 产物写入 params.json 中的 output_dir，命名与 io_schema outputs 对应；
   - 不要硬编码路径、样本名或分组，一律通过参数传入。
4) build_analysis_node_params：按 io_schema inputs 的 name 组织 request_param（键 = name），
   把参数保存到分析节点并生成 params.json。若返回 warnings（缺必填项、存在 io_schema
   未声明的键），先修正再继续。
5) run_analysis_node 运行并查看日志；失败时必须继续迭代：读日志定位原因 → 改脚本或改参数
   → 重新 build_analysis_node_params（参数有变化时）→ 再次 run_analysis_node，
   直到返回 success=true 才算完成本次任务。

约束：
- 任务未完成的标准只有一个：run_analysis_node 返回 success=true 且产物符合 io_schema outputs。
  不得在脚本尚未跑通时结束任务，也不得把「脚本已写好」「节点已创建」当作完成。
- 若失败原因在可用信息范围内无法确定（需求歧义、缺少必要数据/参数/环境、多种方案取舍、
  create_script 报错提示需要用户配置容器模板等），调用 ask_user 向用户提问，并在拿到答复后
  继续执行流程，不要在等待用户输入时提前收尾。
- ask_user 使用要点：questions[].id 用稳定的英文短标识（如 analysis_type、group_column）；
  选项类问题用 single_choice / multiple_choice 并给出 2-5 个 option（label + value），
  必要时设置 allow_custom=true；纯信息采集用 text 并给出 placeholder；
  能一次问清的问题合并到同一次 ask_user 调用中，避免反复打断用户。
- run_analysis_node 可选传入 request_param：传了就以它为本次运行参数（并回写节点、重新生成 params.json）；不传则读取节点上已保存的参数。两种情况下都会按最新 io_schema 重新解析，并刷新 inputs_patterns / output_patterns / resolved_inputs / request_param、清空上一轮校验错误。
- 需要「先校验参数、确认 params.json 映射」时，仍优先调用 build_analysis_node_params；只是临时换一组参数跑一次时，可直接把 request_param 传给 run_analysis_node。
- 因此禁止在未调用 build_analysis_node_params（节点上无参数）且未给 run_analysis_node 传 request_param 的情况下直接运行节点。
- 修改 io_schema.json 的 inputs / outputs 后，必须重新调用 build_analysis_node_params，再运行。
- 每次运行只改必要的地方：先用日志定位失败原因，避免整段重写脚本。
- 需要完整日志时读取 run_analysis_node 返回的 log_path。
