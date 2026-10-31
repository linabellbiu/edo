<script lang="ts">
import {defineComponent,h,ref,watch} from 'vue'
import {Select} from 'ant-design-vue'

export default defineComponent({
  name:'KubeEditorSelect',
  inheritAttrs:false,
  props:{active:{type:Boolean,required:true},disabled:{type:Boolean,default:undefined}},
  emits:['dropdownVisibleChange','inputKeyDown'],
  setup(props,{attrs,slots,emit,expose}){
    const popupOpen=ref(false),select=ref<{focus:()=>void;blur:()=>void}>()
    function changeOpen(open:boolean){
      if(open&&(!props.active||props.disabled)||popupOpen.value===open)return
      popupOpen.value=open
      emit('dropdownVisibleChange',open)
    }
    watch(()=>[props.active,props.disabled],()=>{
      if(!props.active||props.disabled)changeOpen(false)
    },{flush:'sync'})
    function inputKeyDown(event:KeyboardEvent){
      // 公开 open 状态负责关下拉；只消费已打开下拉的 ESC，不传给编辑器关闭逻辑。
      if(event.key==='Escape'&&popupOpen.value){
        event.stopPropagation()
        event.preventDefault()
        changeOpen(false)
      }
      emit('inputKeyDown',event)
    }
    expose({focus:()=>select.value?.focus(),blur:()=>select.value?.blur()})
    return()=>h(Select,{...attrs,ref:select,open:popupOpen.value,disabled:!props.active||props.disabled,onDropdownVisibleChange:changeOpen,onInputKeyDown:inputKeyDown},slots)
  },
})
</script>
