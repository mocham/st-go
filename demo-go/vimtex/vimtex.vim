" Explicit preview navigation; no cursor-following autocommands or key remaps.
if !exists('*echoraw')
  finish
endif

function! s:Seek(value) abort
  let target = empty(a:value) ? line('.') : str2nr(a:value)
  if target < 1
    echoerr 'VimtexPreview expects a positive source line'
    return
  endif
  call echoraw("\<Esc>Pregion secondary seek " . target . "\<Esc>\\")
endfunction

function! s:Scroll(value) abort
  if a:value !~# '^[+-]\=\d\+$'
    echoerr 'VimtexScroll expects a signed wheel-step count'
    return
  endif
  call echoraw("\<Esc>Pregion secondary scroll " . str2nr(a:value) . "\<Esc>\\")
endfunction

command! -nargs=? VimtexPreview call <SID>Seek(<q-args>)
command! -nargs=1 VimtexScroll call <SID>Scroll(<q-args>)
