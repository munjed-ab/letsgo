const out=[];const ok=(n,c,x='')=>out.push((c?'PASS ':'FAIL ')+n+(c?'':'  -> '+x));
let answer='';window.prompt=()=>answer;window.confirm=()=>true;window.alert=m=>out.push('ALERT '+m);
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
async function until(f,ms=10000){const t=Date.now();while(Date.now()-t<ms){try{const v=f();if(v)return v}catch(e){}await sleep(40)}return false}
const click=el=>el.dispatchEvent(new MouseEvent('click',{bubbles:true}));
const B=(text)=>[...document.querySelectorAll('button')].find(b=>b.textContent.trim()===text);
const rows=()=>[...document.querySelectorAll('.row[data-t]')];
const j=p=>fetch(p).then(r=>r.json());
const setq=v=>{const q=document.getElementById('q');q.value=v;q.dispatchEvent(new Event('input',{bubbles:true}))};
(async()=>{try{
 await until(()=>document.querySelectorAll('.row[data-dir]').length>=2);
 const hb=document.querySelector('header').getBoundingClientRect(),mb=document.getElementById('view').getBoundingClientRect(),sb=document.getElementById('side').getBoundingClientRect();
 ok('desktop layout: sidebar on the left, player bar along the bottom',sb.right<=mb.left+1&&hb.top>=mb.bottom-1&&Math.round(hb.bottom)===innerHeight,JSON.stringify([sb,mb,hb,innerHeight]));
 ok('two folders at the top',document.querySelectorAll('.row[data-dir]').length===2);
 ok('untagged root file shows its file name',document.body.textContent.includes('plain'));

 click(document.querySelector('.row[data-dir="Album"]'));
 await until(()=>document.querySelector('.row[data-t] img'));
 ok('tagged title shown instead of file name',rows().length===2&&rows().every(r=>r.querySelector('b').textContent==='Tagged Song'),rows().map(r=>r.textContent));
 ok('artist shown under the title',rows()[0].querySelector('small').textContent==='The Artist');
 ok('cover art loads',await until(()=>{const i=document.querySelector('.row[data-t] img');return i&&i.complete&&i.naturalWidth>0}));

 click(rows()[0].querySelector('.heart'));
 ok('heart favorites the song',await until(()=>rows()[0].querySelector('.heart.on')));
 ok('favorite saved on the node',(await j('/api/lists')).favorites.length===1);

 click(document.querySelector('.crumb button[data-dir=""]'));await sleep(60);
 setq('artist');await sleep(80);
 ok('search finds songs by ARTIST across folders',rows().length===3,rows().length);
 setq('sweep');await sleep(80);
 ok('search still finds by file name',rows().length===1,rows().length);
 setq('');await sleep(80);

 // ---- bulk selection ----
 click(B('Select'));await sleep(60);
 ok('selection mode shows checkboxes',document.querySelectorAll('.ck,.fck').length>0);
 click(B('Select all'));await sleep(60);
 ok('Select all counts every song under the folder',document.body.textContent.includes('5 selected'),document.querySelector('.sel')?.textContent);
 click(B('♥ Favorite'));await sleep(500);
 ok('all 5 are favorites',(await j('/api/lists')).favorites.length===5);
 click(B('Select'));await sleep(60);click(B('Select all'));await sleep(60);
 ok('button offers Unfavorite when all are favorites',!!B('♡ Unfavorite'));
 click(B('♡ Unfavorite'));await sleep(400);
 ok('bulk unfavorite',(await j('/api/lists')).favorites.length===0);

 // custom selection: tick a whole folder and one song
 click(B('Select'));await sleep(60);
 click(document.querySelector('.row[data-dir="Rock"]'));await sleep(60);
 ok('ticking a folder selects everything in it',document.body.textContent.includes('2 selected'),document.querySelector('.sel')?.textContent);
 click(document.querySelector('.row[data-t="plain.mp3"]'));await sleep(60);
 ok('and a single song can be added',document.body.textContent.includes('3 selected'));
 click(document.querySelector('.row[data-t="plain.mp3"]'));await sleep(60);
 ok('and removed again',document.body.textContent.includes('2 selected'));

 // ---- playlists ----
 answer='Road';click(B('＋ Add to playlist'));await until(()=>document.getElementById('dlg').open);
 click([...document.querySelectorAll('#dlg button')].find(b=>b.textContent.includes('New playlist')));
 await until(async()=>true);await sleep(600);
 let l=await j('/api/lists');
 ok('new playlist created from a selection, with the folder tracks',l.playlists.length===1&&l.playlists[0].name==='Road'&&l.playlists[0].tracks.length===2,JSON.stringify(l.playlists));
 ok('selection cleared after the action',!document.querySelector('.sel'),document.querySelector('.sel')?.textContent+' | selecting='+selecting+' size='+sel.size);

 click(document.querySelector('nav button[data-tab="pl"]'));await sleep(100);
 answer='Trips';click(B('＋ Folder'));await sleep(400);
 answer='Gym';click(document.querySelector('button[data-a="newplin"]'));await sleep(500);
 l=await j('/api/lists');
 ok('folder and a playlist inside it',l.folders[0]==='Trips'&&l.playlists.some(p=>p.name==='Gym'&&p.folder==='Trips'),JSON.stringify(l));
 ok('playlists screen lists both',document.body.textContent.includes('Road')&&document.body.textContent.includes('Gym')&&document.body.textContent.includes('Trips'));

 click([...document.querySelectorAll('.row[data-pl]')].find(r=>r.textContent.includes('Road')));await sleep(150);
 ok('playlist opens with its songs',(document.body.textContent.match(/Tagged Song|sweep/g)||[]).length>=1&&rows().length>=1,rows().length);
 click(B('Select'));await sleep(60);click(B('Select all'));await sleep(60);click(B('Remove'));await sleep(500);
 l=await j('/api/lists');
 ok('bulk remove from a playlist',l.playlists.find(p=>p.name==='Road').tracks.length===0,JSON.stringify(l.playlists));

 answer='Road trip';click(B('Rename'));await sleep(400);
 ok('rename playlist',(await j('/api/lists')).playlists.some(p=>p.name==='Road trip'));
 click(B('Delete'));await sleep(400);
 ok('delete playlist',(await j('/api/lists')).playlists.length===1);
 click(document.querySelector('nav button[data-tab="pl"]'));await sleep(100);
 click(document.querySelector('button[data-a="delfolder"]'));await sleep(400);
 l=await j('/api/lists');
 ok('deleting a folder keeps its playlists (moved to top level)',l.folders.length===0&&l.playlists.length===1&&l.playlists[0].folder==='');

 // ---- Most Played ----
 click(document.querySelector('nav button[data-tab="pl"]'));await sleep(100);
 ok('Most Played is a playlist, counting only songs that still exist',document.querySelector('.row[data-most]')&&document.querySelector('.row[data-most]').textContent.includes('1 songs'),document.getElementById('view').textContent);
 click(document.querySelector('.row[data-most]'));await sleep(150);
 ok('Most Played lists the song with its play count',rows().length===1&&rows()[0].textContent.includes('5 plays'),document.getElementById('view').textContent);
 ok('and cannot be renamed or deleted',!B('Rename')&&!B('Delete'));
 click(B('‹ Playlists'));await sleep(100);

 // ---- play + timeline ----
 click(document.querySelector('nav button[data-tab="music"]'));await sleep(100);
 click(document.querySelector('.row[data-dir="Album"]'));await sleep(100);
 click(rows()[0]);
 const nodePlaying=async()=>{for(let i=0;i<60;i++){if((await j('/api/state')).player.playing)return true;await sleep(50)}return false};
 ok('tapping a song plays it (button flips at once, the node really plays)',document.getElementById('bt').textContent==='⏸'&&await nodePlaying());
 ok('header shows the tag title and artist',await until(()=>document.getElementById('ti').textContent==='Tagged Song'&&document.getElementById('ar').textContent.includes('The Artist')),document.getElementById('ti').textContent);
 // the timeline needs a song longer than the 1 s tagged ones: Rock/sweep.ogg is 3 s
 click(document.querySelector('nav button[data-tab="music"]'));await sleep(100);
 click(document.querySelector('.crumb button[data-dir=""]'));await sleep(60);
 click(document.querySelector('.row[data-dir="Rock"]'));await sleep(100);
 click(document.querySelector('.row[data-t="Rock/sweep.ogg"]'));
 ok('timeline knows the length',await until(()=>!document.getElementById('sk').disabled&&document.getElementById('du').textContent==='0:03'),document.getElementById('du').textContent);
 const sk=document.getElementById('sk');sk.value=500;sk.dispatchEvent(new Event('change',{bubbles:true}));await sleep(500);
 const st=await j('/api/state');
 ok('dragging the timeline seeks',st.now.track==='Rock/sweep.ogg'&&st.now.elapsed>1.0,JSON.stringify(st.now));

 // ---- devices ----
 click(document.querySelector('nav button[data-tab="dev"]'));await sleep(200);
 ok('devices tab lists this device as listening',document.body.textContent.includes('webtest'));
 document.getElementById('src').value='__EXTRA__';click(B('Add folder'));await sleep(900);
 ok('adding a music folder adds its songs',(await j('/api/library')).length===__LIB__,(await j('/api/library')).length);
 if(__LIB__===7){   // ---- video: a song with a picture that stays closed until asked for ----
  click(document.querySelector('nav button[data-tab="music"]'));await sleep(100);
  click(document.querySelector('.crumb button[data-dir=""]'));await sleep(60);
  click(document.querySelector('.row[data-dir="extra"]'));await sleep(100);
  const clip=document.querySelector('.row[data-t="extra/clip.mp4"]');
  ok('a video is listed like a song, with a VIDEO mark; a song has none',!!clip&&!!clip.querySelector('.vd')&&!document.querySelector('.row[data-t="extra/extra1.mp3"] .vd'));
  click(document.querySelector('.row[data-t="extra/extra1.mp3"]'));
  ok('the video button is not there for a song',await until(()=>document.getElementById('bv').hidden));
  click(clip);
  ok('playing a video does not open it: the button appears, the picture stays closed',await until(()=>!document.getElementById('bv').hidden)&&!document.body.classList.contains('vid')&&!document.getElementById('vid').getAttribute('src'));
  click(document.getElementById('bv'));
  ok('the button opens the video view, which loads the file from this node',document.body.classList.contains('vid')&&document.getElementById('vid').getAttribute('src')==='/api/video?t=extra%2Fclip.mp4',document.getElementById('vid').getAttribute('src'));
  ok('and the browser can read it',await until(()=>document.getElementById('vid').readyState>=1&&document.getElementById('vid').videoWidth===96),document.getElementById('vid').readyState);
  ok('the list is out of the way while the video is open',getComputedStyle(document.getElementById('view')).display==='none');
  ok('the video view has a full screen button',!!document.getElementById('vfs')&&getComputedStyle(document.getElementById('vfs')).display!=='none');
  click(document.getElementById('vid'));await sleep(400);
  ok('a click on the picture pauses, another plays',(await j('/api/state')).player.playing===false);
  click(document.getElementById('vid'));await sleep(400);
  ok('and plays again',(await j('/api/state')).player.playing===true);
  {let fsOk=true;try{await document.getElementById('vidbox').requestFullscreen()}catch(e){fsOk=false}   // headless Chrome may refuse: then this is skipped, not failed
   if(fsOk){ok('full screen shows only the video box',document.fullscreenElement===document.getElementById('vidbox'));
    await document.exitFullscreen();await sleep(200);ok('and leaves again',!document.fullscreenElement)}}
  {const box=document.getElementById('vidbox'),exits=[];const realExit=document.exitFullscreen;   // full screen outlives the track
   Object.defineProperty(document,'fullscreenElement',{get:()=>box,configurable:true});document.exitFullscreen=()=>{exits.push(1);return Promise.resolve()};
   click(document.querySelector('.row[data-t="extra/extra1.mp3"]')||document.querySelector('.row[data-t]'));   // a song
   ok('a song while full screen shows its cover and leaves full screen alone',await until(()=>!isVideo(now.track)&&!document.getElementById('vcover').hidden&&document.getElementById('vid').hidden&&document.body.classList.contains('vid'))&&exits.length===0,exits.length);
   delete document.fullscreenElement;document.exitFullscreen=realExit;document.dispatchEvent(new Event('fullscreenchange'));await sleep(200);
   ok('and when full screen ends the list is back',!document.body.classList.contains('vid')&&document.getElementById('vcover').hidden)}
  click(document.querySelector('nav button[data-tab="fav"]'));await sleep(100);
  ok('going back to the music closes it',!document.body.classList.contains('vid')&&!document.getElementById('vid').getAttribute('src'));
  await post('/api/control?cmd=pause');
  click(document.querySelector('nav button[data-tab="music"]'));await sleep(100);   // the timeline check below and the player bar after it need sweep.ogg to be the last song here
  click(document.querySelector('.crumb button[data-dir=""]'));await sleep(60);
  click(document.querySelector('.row[data-dir="Rock"]'));await sleep(100);
  click(document.querySelector('.row[data-t="Rock/sweep.ogg"]'));await sleep(300);
  await post('/api/control?cmd=pause');
  click(document.querySelector('nav button[data-tab="dev"]'));await sleep(300);
 }
 click(document.querySelector('button[data-a="rmsrc"][data-d="__EXTRA__"]'));await sleep(900);
 ok('removing the folder removes them',(await j('/api/library')).length===5);
 const lat=document.getElementById('lat');lat.value=-30;lat.dispatchEvent(new Event('change',{bubbles:true}));await sleep(300);
 ok('sync offset saved',(await j('/api/state')).latencyMs===-30);

 // ---- another device: browse it, play on it, tune its sync, all from this window ----
 const pick=document.getElementById('dev');
 ok('device picker starts on this device',pick.value==='');
 answer='127.0.0.1';pick.value='__addr';pick.dispatchEvent(new Event('change',{bubbles:true}));answer='';
 ok('picking a device by address switches to it',await until(()=>dev==='127.0.0.1'&&loaded&&lib.length===1),dev+' '+lib.length);
 click(document.querySelector('nav button[data-tab="music"]'));await sleep(150);
 ok("the other device's music is listed, not this one's",await until(()=>{const v=document.getElementById('view').textContent;return v.includes('Phone Song')&&!v.includes('Album')}),document.getElementById('view').textContent);
 click(document.querySelector('.row[data-t]'));
 let ps;for(let i=0;i<60&&!(ps&&ps.player.playing);i++){await sleep(100);ps=await j('/dev/127.0.0.1/api/state')}
 ok('playing from this window starts the song on the other device',ps&&ps.player.playing&&ps.player.track==='Phone Song.mp3',JSON.stringify(ps&&ps.player));
 ok('the device that was casting stops, so everyone follows the new source',(await j('/api/state')).player.playing===false);
 ok('header shows what plays on the controlled device',await until(()=>document.getElementById('ti').textContent==='Phone Song'),document.getElementById('ti').textContent);
 const vol=document.getElementById('vol');vol.value=33;vol.dispatchEvent(new Event('change',{bubbles:true}));await sleep(400);
 ok('volume goes to the device that plays',(await j('/dev/127.0.0.1/api/state')).volume===33&&(await j('/api/state')).volume===100);
 click(document.querySelector('nav button[data-tab="dev"]'));await sleep(500);
 ok('devices tab has a card per device',document.querySelectorAll('.card').length===2,document.getElementById('view').textContent);
 const gh=document.querySelector('a.gh');ok('the devices tab has the GitHub badge, which opens the repository in a new tab',gh&&gh.href==='https://github.com/munjed-ab/letsgo'&&gh.target==='_blank'&&gh.rel.includes('noopener'),gh&&gh.outerHTML.slice(0,200));
 ok('the controlled device is marked',document.querySelectorAll('.card.cur').length===1&&document.querySelector('.card.cur').textContent.includes('phonetest'),document.getElementById('view').textContent);
 const off=document.querySelector('input[data-lat="127.0.0.1"]');off.value=40;off.dispatchEvent(new Event('change',{bubbles:true}));await sleep(500);
 ok("the other device's sync offset is set from here, this one's is untouched",(await j('/dev/127.0.0.1/api/state')).latencyMs===40&&(await j('/api/state')).latencyMs===-30);
 const bad=await fetch('/dev/8.8.8.8/api/state');ok('proxy refuses addresses outside the local network',bad.status===403,bad.status);
 answer='127.0.0.3';pick.value='__addr';pick.dispatchEvent(new Event('change',{bubbles:true}));answer='';
 ok('a device that is gone says so, and the player bar keeps working',await until(()=>dev==='127.0.0.3'&&loadErr&&document.getElementById('ti').textContent==='sweep'),dev+' '+loadErr+' '+document.getElementById('ti').textContent);
 pick.value='';pick.dispatchEvent(new Event('change',{bubbles:true}));
 ok("switching back shows this device's music",await until(()=>dev===''&&loaded&&lib.length===5),dev+' '+lib.length);
}catch(e){out.push('EXCEPTION '+e+' '+(e.stack||''))}
document.getElementById('result').textContent=out.join('\n')+'\nDONE';parent.postMessage(document.getElementById('result').textContent,'*');})();
