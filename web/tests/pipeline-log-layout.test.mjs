import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {test} from 'node:test'
import {compileStyle,compileTemplate,parse} from '@vue/compiler-sfc'

const descriptor=parse(readFileSync(new URL('../src/components/PipelineLogDrawer.vue',import.meta.url),'utf8')).descriptor
const compiled=compileStyle({source:descriptor.styles[0].content,filename:'PipelineLogDrawer.vue',id:'data-v-log-layout',scoped:true})
assert.deepEqual(compiled.errors,[])
const css=compiled.code

test('日志区跟随抽屉剩余高度伸缩，不依赖固定视口高度或外层二次滚动',()=>{
 const template=compileTemplate({source:descriptor.template.content,filename:'PipelineLogDrawer.vue',id:'log-layout'})
 assert.deepEqual(template.errors,[])
 const drawer=descriptor.template.ast.children.find(node=>node.tag==='a-drawer')
 const bodyStyle=drawer.props.find(prop=>prop.type===7&&prop.name==='bind'&&prop.arg?.content==='body-style')
 assert.ok(bodyStyle,'抽屉正文需要独立的有界 flex 布局')
 for(const rule of [/display:\s*'flex'/,/flexDirection:\s*'column'/,/minHeight:\s*0/,/overflow:\s*'hidden'/])assert.match(bodyStyle.exp.content,rule)
 assert.match(css,/\.log-meta\[data-v-log-layout\]\s*\{[^}]*flex:\s*none/)
 assert.match(css,/\.log-viewport\[data-v-log-layout\]\s*\{[^}]*flex:\s*1[^}]*min-height:\s*0/)
 assert.doesNotMatch(css,/height:\s*calc\(100vh/)
})

test('长日志与标识允许收缩换行，手机宽度下正文独占第二行',()=>{
 assert.match(css,/grid-template-columns:\s*75px 90px minmax\(0,\s*1fr\)/)
 assert.match(css,/\.log-line code\[data-v-log-layout\]\s*\{[^}]*min-width:\s*0[^}]*white-space:\s*pre-wrap[^}]*overflow-wrap:\s*anywhere/)
 assert.match(css,/@media\s*\(max-width:\s*640px\)[\s\S]*grid-template-columns:\s*75px minmax\(0,\s*1fr\)/)
 assert.match(css,/@media\s*\(max-width:\s*640px\)[\s\S]*\.log-line code\[data-v-log-layout\]\s*\{[^}]*grid-column:\s*1\s*\/\s*-1/)
 assert.doesNotMatch(css,/word-break:\s*break-all|text-overflow:\s*ellipsis/)
})

test('日志滚动区有明确键盘入口、可访问名称和主题焦点样式',()=>{
 const viewport=descriptor.template.ast.children.find(node=>node.tag==='a-drawer').children.find(node=>node.tag==='div'&&node.props.some(prop=>prop.name==='class'&&prop.value?.content==='log-viewport'))
 assert.ok(viewport)
 assert.ok(viewport.props.some(prop=>prop.name==='tabindex'&&prop.value?.content==='0'))
 assert.ok(viewport.props.some(prop=>prop.name==='role'&&prop.value?.content==='region'))
 assert.ok(viewport.props.some(prop=>prop.type===7&&prop.name==='bind'&&prop.arg?.content==='aria-label'&&prop.exp?.content==='title'))
 assert.match(css,/\.log-viewport\[data-v-log-layout\]:focus-visible\s*\{[^}]*outline:\s*2px solid var\(--edo-primary\)/)
})
