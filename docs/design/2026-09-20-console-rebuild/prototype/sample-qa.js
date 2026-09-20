async (page) => {
  const errors = [];
  page.on('pageerror', error => errors.push(error.message));
  await page.goto('http://127.0.0.1:18422/prototype.html#/home');
  await page.reload();
  await page.locator('#reset-demo').click();
  const results=[];
  for (const id of [1024,1025,1026,1027]) {
    await page.goto('http://127.0.0.1:18422/prototype.html#/datasets/24/samples/'+id);
    await page.waitForFunction(value => route === '/datasets/24/samples/'+value, id);
    const data=await page.evaluate(value=>({content:document.querySelector('.sample-text').textContent,expected:DEMO_SAMPLES[value].question,wrongEvidence:value!==1024&&Boolean(document.querySelector('a[href="#/evaluations/36/items/1024"]')),status:document.querySelector('.pill-row').textContent}),id);
    if(data.content!==data.expected||data.wrongEvidence||(id===1027&&!data.status.includes('已隔离')))throw Error('Sample context mismatch '+id);
    if(id===1024)await page.screenshot({path:'output/playwright/console-rebuild/screens/D04.png',fullPage:true});
    results.push({id,passed:true});
  }
  await page.goto('http://127.0.0.1:18422/prototype.html#/datasets/24/samples/9999');
  await page.getByRole('heading',{name:'没有找到这个页面'}).waitFor();
  results.push({id:9999,passed:true,expected:'404'});
  if(errors.length)throw Error(errors.join(';'));
  await page.goto('http://127.0.0.1:18422/prototype.html#/home');
  return {samples:results,errors};
}
