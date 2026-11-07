import { parseAllDocuments } from 'yaml'
import { parse as parseToml, TomlError } from 'smol-toml'
import { parse as parseIni } from 'ini'
import { jsonLanguage } from '@codemirror/lang-json'
export type ConfigFormat = 'yaml' | 'toml' | 'env' | 'ini' | 'json' | 'text'
export interface ConfigIssue { from: number; to: number; line: number; column: number; message: string; severity: 'error' | 'warning' }
export const configFormats = ['yaml','toml','env','ini','json','text'].map(value => ({value: value as ConfigFormat,label:value==='text'?'纯文本':value.toUpperCase()}))
export function configurationFormat(kind: string): ConfigFormat | undefined { return kind.startsWith('config_') && configFormats.some(v=>v.value===kind.slice(7)) ? kind.slice(7) as ConfigFormat : undefined }
export function guessConfigFormat(name: string): ConfigFormat | undefined { const lower=name.toLowerCase();return lower==='.env'||lower.endsWith('.env')||lower.startsWith('.env.')?'env':({yaml:'yaml',yml:'yaml',toml:'toml',ini:'ini',json:'json',txt:'text'} as Record<string,ConfigFormat>)[lower.split('.').pop()||''] }
export function configIssue(source:string,message:string,from=0,to=from+1,severity:ConfigIssue['severity']='error'):ConfigIssue { from=Math.min(source.length,Math.max(0,from));const before=source.slice(0,from);return {from,to:Math.min(source.length,Math.max(from,to)),line:before.split('\n').length,column:from-before.lastIndexOf('\n'),message,severity} }
export function checkConfigText(source:string,format:ConfigFormat):ConfigIssue[] {
 if(new TextEncoder().encode(source).length>65536)return [configIssue(source,'配置内容不能超过 64 KiB')]
 if(source.includes('\0'))return [configIssue(source,'配置包含无效字符',source.indexOf('\0'))]
 if(format==='text')return []
 if(format==='yaml'){try{return parseAllDocuments(source,{prettyErrors:false,uniqueKeys:true,strict:true}).flatMap(doc=>[...doc.errors,...doc.warnings].map(error=>configIssue(source,error.code==='DUPLICATE_KEY'?'YAML 存在重复的键':'YAML 语法无效，请检查缩进、引号及括号',error.pos[0],error.pos[1],doc.errors.includes(error)?'error':'warning'))).slice(0,20)}catch{return [configIssue(source,'YAML 语法无效')]}}
 if(format==='toml'){try{parseToml(source,{integersAsBigInt:'asNeeded',maxDepth:64});return []}catch(error){let from=0;if(error instanceof TomlError)from=source.split('\n').slice(0,Math.max(0,error.line-1)).reduce((total,line)=>total+line.length+1,0)+Math.max(0,error.column-1);return [configIssue(source,'TOML 语法无效，请检查键、值及表定义',from)]}}
 if(format==='json'){const issues:ConfigIssue[]=[];jsonLanguage.parser.parse(source).iterate({enter(node){if(node.type.isError&&issues.length<20)issues.push(configIssue(source,'JSON 语法无效，请检查引号、逗号及括号',node.from,Math.max(node.from+1,node.to)))}});return issues}
 const issues:ConfigIssue[]=[],keys=new Set<string>();let section='',start=0
 for(const original of source.split('\n')){const line=original.replace(/\r$/,''),trimmed=line.trim(),from=start+line.length-line.trimStart().length
  if(trimmed&&!trimmed.startsWith('#')&&!(format==='ini'&&trimmed.startsWith(';'))){
   if(format==='ini'&&trimmed.startsWith('[')){if(!/^\[[^\]\r\n]+\]\s*(?:[;#].*)?$/.test(trimmed))issues.push(configIssue(source,'INI 分节应使用 [节名称]',from,start+line.length));else section=trimmed.slice(1,trimmed.indexOf(']'))}
   else {const separator=line.indexOf('='),key=separator<0?'':line.slice(0,separator).trim();if(!key||format==='env'&&!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key))issues.push(configIssue(source,format==='env'?'ENV 每行应为 KEY=value':'INI 配置项应为 key=value',from,start+line.length));else{const identity=JSON.stringify([section,key]);if(keys.has(identity)&&!key.endsWith('[]'))issues.push(configIssue(source,'存在重复的配置项',from,from+key.length,format==='ini'?'warning':'error'));keys.add(identity)}}
  }start+=original.length+1;if(issues.length>=20)break
 }
 if(format==='ini'&&!issues.some(v=>v.severity==='error')){try{parseIni(source)}catch{issues.push(configIssue(source,'INI 配置结构冲突，请检查分节与键名称'))}}
 return issues
}

export function formatConfigText(source:string,format:ConfigFormat):string|undefined {
 if(format!=='yaml'||checkConfigText(source,format).some(issue=>issue.severity==='error'))return undefined
 return parseAllDocuments(source,{prettyErrors:false}).map(document=>document.toString({lineWidth:0})).join('')
}
