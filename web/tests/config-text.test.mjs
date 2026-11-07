import assert from 'node:assert/strict'
import {test} from 'node:test'
import {checkConfigText,guessConfigFormat,formatConfigText} from '../src/utils/configText.ts'

test('各格式使用实际解析器检查有效输入，不把应用语义当成语法',()=>{
 for(const [format,source] of [['yaml','app:\n  port: 1234\n'],['yaml','- one\n- two\n'],['toml','port = 9223372036854775807\n[app]\nname="demo"'],['env','TOKEN=literal$token\nEMPTY=\n'],['ini','[app]\nport=8080\n'],['json','{"port":8080}'],['text','unstructured arbitrary text']])assert.deepEqual(checkConfigText(source,format),[],format)
})
test('语法错误具备准确位置，诊断不回显敏感正文',()=>{
 for(const [format,source] of [['yaml','app:\n  secret: [sensitive-token'],['toml','name="sensitive-token"\nport = ['],['env','GOOD=one\nsensitive-token'],['ini','[app\nsensitive-token'],['json','{"secret":"sensitive-token",}']]){
  const issues=checkConfigText(source,format);assert.ok(issues.some(v=>v.severity==='error'),format);assert.ok(issues.every(v=>v.line>=1&&v.column>=1&&v.from>=0&&v.to<=source.length));assert.doesNotMatch(JSON.stringify(issues),/sensitive-token/)
 }
 assert.equal(checkConfigText('A=1\ninvalid','env')[0].line,2)
})
test('重复配置项、INI 方言和内容边界有明确处理',()=>{
 assert.equal(checkConfigText('a: 1\na: 2','yaml')[0].severity,'error')
 assert.equal(checkConfigText('a=1\na=2','env')[0].severity,'error')
 assert.equal(checkConfigText('[app]\na=1\na=2','ini')[0].severity,'warning')
 assert.equal(checkConfigText('a=[1 #','toml')[0].severity,'error','恶意不完整输入不能挂起解析器')
 assert.ok(checkConfigText('你'.repeat(22000),'text').length)
 assert.ok(checkConfigText('a\0b','text').length)
})
test('常见扩展名推断格式',()=>{
 for(const [name,format] of [['.env','env'],['.prod.env','env'],['app.yml','yaml'],['app.toml','toml'],['app.ini','ini'],['app.json','json'],['notes.txt','text']])assert.equal(guessConfigFormat(name),format)
})

test('格式化保留注释，无效 YAML 不产生替代内容',()=>{
 assert.equal(formatConfigText('a: [broken','yaml'),undefined)
 assert.match(formatConfigText('# 注释\na:   1\n','yaml'),/# 注释/)
 assert.equal(formatConfigText('x = 1','toml'),undefined)
})
