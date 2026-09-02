// Theme Toggle
const prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
if (prefersDark && !localStorage.getItem('theme')) {
  document.documentElement.setAttribute('data-theme', 'dark');
}

// Smooth scroll for anchor links
document.querySelectorAll('a[href^="#"]').forEach(anchor => {
  anchor.addEventListener('click', function (e) {
    const href = this.getAttribute('href');
    if (href !== '#' && href !== '#demo' && href !== '#github' && href !== '#docs') {
      e.preventDefault();
      const target = document.querySelector(href);
      if (target) {
        target.scrollIntoView({
          behavior: 'smooth',
          block: 'start'
        });
      }
    }
  });
});

// Intersection Observer for fade-in animations
if ('IntersectionObserver' in window) {
  const observer = new IntersectionObserver((entries) => {
    entries.forEach(entry => {
      if (entry.isIntersecting) {
        entry.target.style.opacity = '1';
        observer.unobserve(entry.target);
      }
    });
  }, {
    threshold: 0.1
  });

  // Observe feature cards
  document.querySelectorAll('.feature-card').forEach(card => {
    if (!card.classList.contains('fade-in')) {
      card.style.opacity = '0';
      observer.observe(card);
    }
  });
}

// Analytics placeholder (can be integrated with Plausible, Fathom, etc)
window.addEventListener('load', () => {
  console.log('Bravis landing page loaded');
});

// External links
document.querySelectorAll('a[href^="http"]').forEach(link => {
  link.setAttribute('target', '_blank');
  link.setAttribute('rel', 'noopener noreferrer');
});