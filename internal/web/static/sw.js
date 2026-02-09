// onWatch Service Worker — push notifications only, no offline caching.
self.addEventListener('push', function(event) {
  var data = { title: 'onWatch', body: 'Quota alert' };
  if (event.data) {
    try { data = event.data.json(); } catch (e) { data.body = event.data.text(); }
  }
  event.waitUntil(
    self.registration.showNotification(data.title, {
      body: data.body,
      icon: '/static/favicon.svg',
      badge: '/static/favicon.svg',
      tag: 'onwatch-alert',
      renotify: true
    })
  );
});

self.addEventListener('notificationclick', function(event) {
  event.notification.close();
  event.waitUntil(
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function(list) {
      for (var i = 0; i < list.length; i++) {
        if (list[i].url.includes('/') && 'focus' in list[i]) return list[i].focus();
      }
      return clients.openWindow('/');
    })
  );
});
