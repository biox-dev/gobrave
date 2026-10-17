## 技能：analysis-script-workflow

「创建分析脚本 / 分析节点」的完整固定流程、io_schema.json 生成规则（inputs 组件类型、FileSelect
输入输出写法、request_param 示例）以及失败迭代与记忆写入约束，已抽取为内置技能
**analysis-script-workflow**。

当用户要求新建分析脚本 / 分析节点、修改脚本或运行节点时，先调用 `get_skill` 加载
`analysis-script-workflow`，并严格按其步骤执行；需要参考文档时用 `load_skill_resource` 读取该技能目录内的资源。

## 技能：analysis-node-script-editing

「在已创建的分析节点里写 / 改脚本、运行当前节点、按 io_schema outputs 写 outputs.json」的约定
已抽取为内置技能 **analysis-node-script-editing**。

当当前上下文存在分析节点（analysis_node_id 非空）、用户要求写代码 / 改代码 / 跑节点时，先调用
`get_skill` 加载 `analysis-node-script-editing` 并按其执行。

{% if analysis_node_id %}
## Analysis Node ID: {{ analysis_node_id }}

当前脚本 {{script_file_path}}，参数 {{params_file_path}}，io_schema {{io_schema_path}}。
{% endif %}

