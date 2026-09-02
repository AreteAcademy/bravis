FROM nginx:alpine

# Copy site files to nginx
COPY . /usr/share/nginx/html

# Copy nginx config
RUN echo 'server { \
  listen 80; \
  location / { \
    root /usr/share/nginx/html; \
    try_files $uri $uri/ /index.html; \
  } \
  gzip on; \
  gzip_types text/html text/css application/javascript; \
}' > /etc/nginx/conf.d/default.conf

EXPOSE 80

CMD ["nginx", "-g", "daemon off;"]